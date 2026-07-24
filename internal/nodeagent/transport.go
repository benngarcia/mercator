package nodeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/nodeapi"
)

// HTTPTransport reaches the control plane over the node protocol. Every call is
// outbound: the agent opens the session and posts what it owes, and nothing
// ever connects to the machine.
type HTTPTransport struct {
	baseURL string
	client  *http.Client
	// stream is a client without a response timeout, because the session is a
	// long-lived read rather than a request that should finish.
	stream *http.Client
}

func NewHTTPTransport(baseURL string, client *http.Client) *HTTPTransport {
	if client == nil {
		client = http.DefaultClient
	}
	streaming := *client
	streaming.Timeout = 0
	return &HTTPTransport{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  client,
		stream:  &streaming,
	}
}

func (transport *HTTPTransport) Enroll(ctx context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error) {
	var response nodeapi.EnrollmentResponse
	if err := transport.post(ctx, "/v1/node-agent/enroll", "", request, &response); err != nil {
		return capability.Enrollment{}, err
	}
	enrollment := capability.Enrollment{
		NodeID:       response.NodeID,
		SessionToken: response.SessionToken,
		FencingToken: response.FencingToken,
	}
	var err error
	if enrollment.SessionExpires, err = parseTime(response.SessionExpires); err != nil {
		return capability.Enrollment{}, err
	}
	if enrollment.LeaseExpires, err = parseTime(response.LeaseExpires); err != nil {
		return capability.Enrollment{}, err
	}
	return enrollment, nil
}

// Session holds the command stream open until the connection ends or ctx is
// cancelled. Commands arrive as newline-delimited JSON.
func (transport *HTTPTransport) Session(ctx context.Context, nodeID, sessionToken string, commands chan<- node.Command) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.baseURL+"/v1/node-agent/"+nodeID+"/session", http.NoBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	response, err := transport.stream.Do(request)
	if err != nil {
		return fmt.Errorf("open node session: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("open node session: %s: %s", response.Status, readSnippet(response.Body))
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var command node.Command
			if decodeErr := json.Unmarshal(line, &command); decodeErr != nil {
				return fmt.Errorf("decode node command: %w", decodeErr)
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (transport *HTTPTransport) SendEvents(ctx context.Context, nodeID, sessionToken string, events []node.Event) error {
	return transport.post(ctx, "/v1/node-agent/"+nodeID+"/events", sessionToken, nodeapi.EventBatch{Events: events}, nil)
}

func (transport *HTTPTransport) SendResult(ctx context.Context, nodeID, sessionToken string, result node.Result) error {
	return transport.post(ctx, "/v1/node-agent/"+nodeID+"/results", sessionToken, result, nil)
}

func (transport *HTTPTransport) post(ctx context.Context, path, sessionToken string, body, into any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if sessionToken != "" {
		request.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	response, err := transport.client.Do(request)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("post %s: %s: %s", path, response.Status, readSnippet(response.Body))
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(into)
}

func readSnippet(body io.Reader) string {
	snippet, _ := io.ReadAll(io.LimitReader(body, 512))
	return strings.TrimSpace(string(snippet))
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
