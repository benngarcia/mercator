# Node Agent Credentials And Sessions

This is the operator's account of what a machine running the Mercator node agent
holds, how long each thing is good for, and what to do when a machine is locked
out. It describes what is implemented today.

## What a machine ever holds

Two credentials, both short lived, and nothing else.

- An **enrollment token**. It is minted by `POST /v1/nodes` (or by whatever
  provisions a machine), returned exactly once in that answer, and handed to the
  machine as part of its bootstrap. It names one node identity and one Rental
  generation, it is redeemable once, and it stays redeemable for 30 minutes.
- A **session credential**. It is what every later exchange is authenticated by:
  the command stream the agent reads work from, the events it reports, and the
  results it settles commands with. It lasts 30 minutes and the agent renews it.

Neither is long lived, and the machine is never given a Mercator API token, a
provider credential, or an inbound listener. Every exchange is opened by the
machine.

## Renewal, and why an agent must not wait for the lapse

The invitation is spent the moment it is redeemed. A machine whose session
credential lapses therefore has nothing left to present: replaying the invitation
is refused twice over, by the store's record that it was already redeemed and by
the signer once the 30 minute window closes.

So the agent renews ahead of the lapse, over
`POST /v1/node-agent/{node}/session/renew`, authenticated by the credential it is
about to stop using. It renews when less than two heartbeat intervals of the
credential's life remain, which is far enough ahead that one failed attempt can
be retried before anything it sends could outlive the credential carrying it.

Renewing is not enrolling and the two are kept apart deliberately:

| | Enrollment | Renewal |
| --- | --- | --- |
| Material presented | the single-use invitation | the current session credential |
| Spends the invitation | yes | no |
| Moves the fencing token | yes | no |
| Durable write | yes | none |
| Available to a retired node | no | no |

A renewal that moved the fencing token would supersede a machine that did
nothing, and the agent's memory of what it has already applied would stop lining
up with the control plane's.

## What bounds a leaked credential

A session credential is a bearer token: whoever holds it can present it, and
renewal will hand that holder a later one. What ends it is the Rental generation
ending. `node.Registry.Retire` refuses that identity a session, a renewal, a
lease renewal, and any further command, and it is terminal: the way back is a
fresh generation with a fresh identity.

Nothing else revokes a credential mid-window today. An operator who believes a
machine's credential is compromised retires the generation.

## When a machine is locked out

A machine that lost its local state (`--state-dir`, `/var/lib/mercator-node` by
default), or that was down for longer than its session window, holds nothing that
opens any door.
This is intended, and the remedy is a fresh invitation for the same identity
rather than a more forgiving enrollment route. The agent then has to be restarted
with the new bootstrap, because the invitation is material a machine is given
rather than material it can ask for.

`node.Registry.Reinvite` is that route, and the machine's current enrollment
stays valid until the new invitation is redeemed, so a healthy node is never cut
off by an invitation nobody uses. **It has no operator surface today.** The only
caller is the orchestrator's own provisioning path, which reinvites an identity
whose first allocation it lost the answer to. An operator whose hand-enrolled
machine lost its state has no supported way to reinvite it and has to invite a
new identity, which loses that machine's history. This gap is
[mercator#211](https://github.com/benngarcia/mercator/issues/211) rather than
something papered over here.

## What the record may never contain

The enrollment token appears in exactly one place: the answer to the invitation
that minted it. It is stored only as a digest (`Record.EnrollmentTokenID`), and
it must never appear in an event, a log line, a projection, or a Run Bundle. Two
Lab rules hold this and both are byte scans over what Mercator actually wrote
rather than checks on the fields somebody remembered:

- `safety.bootstrap_credential_is_short_lived_and_single_use`: one machine holds
  a given credential, it is redeemed once, and it appears nowhere in the record.
- `safety.secrets_absent`: no field named credential, password, or secret; no
  signed URL, which is a bearer credential written as a location; and no
  credential the world minted, whatever field it is filed under.

## Configuration

| Setting | Default | What it is |
| --- | --- | --- |
| `daemon.Config.NodeLease` | 90s | how long the control plane believes a node it has stopped hearing from |
| `daemon.Config.NodeSession` | 30m | how long one session credential authenticates a node |
| agent heartbeat | 20s | how often the agent reports its facts, and the interval renewal is measured against |

The lease and the session are independent clocks. The lease is about liveness and
is renewed by heartbeats; the session is about authentication and is renewed by
the route above. A machine can hold a valid credential and still be believed
lost, and a machine can be heartbeating and still need to renew.
