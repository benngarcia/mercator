package domain

import "testing"

const gigabyte = int64(1e9)

// TestContentThatFitsIsAdmitted is the ordinary machine: it has to fetch what it
// does not hold, it has the room, and holding content stays worth what holding
// content is worth.
func TestContentThatFitsIsAdmitted(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:            182 * gigabyte,
		ReservedBytes:        gigabyte,
		LandBytes:            40*gigabyte + 40*gigabyte/1000,
		EstablishedLandBytes: 40*gigabyte + 40*gigabyte/1000,
	}

	if !demand.Fits() {
		t.Fatalf("a machine with 182GB free refused 41GB of work: %+v", demand)
	}
}

// TestAHostShortOfRoomCannotMakeItOutOfThisRunsContent is the whole rule in one
// case. This machine holds eighteen gigabytes of the image and is thirty-seven
// short of the dataset beside it, and deleting what it holds is no help: every
// byte it gave up is a byte this Run needs back, so the disk ends where it began.
// The only content that would help belongs to somebody else.
func TestAHostShortOfRoomCannotMakeItOutOfThisRunsContent(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:            37 * gigabyte,
		LandBytes:            40*gigabyte + 40*gigabyte/1000,
		EstablishedLandBytes: 40*gigabyte + 40*gigabyte/1000,
	}

	if demand.Fits() {
		t.Fatalf("a machine with 37GB free took 40GB of content: %+v", demand)
	}
}

// TestARunsReservationIsNotSpentOnItsOwnContent is the double spend. A Run that
// declares fifty gigabytes of ephemeral disk and reads a forty gigabyte dataset
// needs ninety on a machine holding neither, because the dataset lands on the
// same disk the reservation is for.
func TestARunsReservationIsNotSpentOnItsOwnContent(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:            60 * gigabyte,
		ReservedBytes:        50 * gigabyte,
		LandBytes:            40 * gigabyte,
		EstablishedLandBytes: 40 * gigabyte,
	}

	if demand.Fits() {
		t.Fatalf("a Run reserving 50GB was admitted onto 60GB with 40GB to land: %+v", demand)
	}
	if want := 90 * gigabyte; demand.RequiredBytes() != want {
		t.Fatalf("required = %d, want the %d the reservation and the content add up to", demand.RequiredBytes(), want)
	}
}

// TestSilenceIsNeverWhatRefusesAMachine is the architectural rule read from the
// disk end. Bytes counted because nobody could say whether they are already here
// are a price, and a machine turned away for them is a machine refused for a
// silence, which is exactly what unknown locality must never become.
func TestSilenceIsNeverWhatRefusesAMachine(t *testing.T) {
	demand := DiskDemand{
		FreeBytes:            10 * gigabyte,
		LandBytes:            40 * gigabyte,
		EstablishedLandBytes: 0,
	}

	if !demand.Fits() {
		t.Fatalf("a machine nobody could enumerate was refused for content it may hold: %+v", demand)
	}
}
