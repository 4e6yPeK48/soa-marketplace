package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/require"
)

func (r *testRun) filteredLogs(output string) []byte {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var event M
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			if id, ok := event["request_id"].(string); ok {
				if _, found := r.requests[id]; found {
					lines = append(lines, r.redact(scanner.Text()))
				}
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
func (s *scenario) failureEvidence() {
	output, err := s.run.command("logs", "--no-color", "--no-log-prefix", "catalog")
	if err == nil {
		logs := s.run.filteredLogs(output)
		s.a.Attachment("Application logs", logs, "text/plain")
		if err := os.WriteFile(filepath.Join(s.run.dir, "application.log"), logs, 0644); err != nil {
			s.t.Errorf("cannot save failure logs")
		}
	}
}
func (s *scenario) verifyLogs() {
	var output string
	records := map[string]M{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		output, err = s.run.command("logs", "--no-color", "--no-log-prefix", "catalog")
		require.NoError(s.t, err)
		scanner := bufio.NewScanner(strings.NewReader(output))
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			event, err := decodeObject(scanner.Bytes())
			if err != nil {
				continue
			}
			id, _ := event["request_id"].(string)
			if req, found := s.run.requests[id]; found {
				for _, key := range []string{"request_id", "method", "endpoint", "status_code", "duration_ms", "user_id", "timestamp"} {
					_, exists := event[key]
					require.True(s.t, exists, "missing audit field %s", key)
				}
				_, err := time.Parse(time.RFC3339Nano, event["timestamp"].(string))
				require.True(s.t, err == nil, "invalid audit timestamp")
				require.False(s.t, amount(event["duration_ms"]).IsNegative())
				if req.Method == "POST" || req.Method == "PUT" || req.Method == "DELETE" {
					_, exists := event["request_body"]
					require.True(s.t, exists, "missing audit request body")
				}
				records[id] = event
			}
		}
		require.NoError(s.t, scanner.Err())
		if len(records) == len(s.run.requests) || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, secret := range s.run.secrets {
		if secret != "" {
			require.False(s.t, strings.Contains(output, secret), "sensitive data leaked into application logs")
		}
	}
	require.NotEmpty(s.t, s.run.requests)
	for id := range s.run.requests {
		_, exists := records[id]
		require.True(s.t, exists, "missing application log for request %s", id)
	}
	logs := s.run.filteredLogs(output)
	s.a.Attachment("Application audit", logs, "text/plain")
	require.NoError(s.t, os.WriteFile(filepath.Join(s.run.dir, "application.log"), logs, 0644))
}
