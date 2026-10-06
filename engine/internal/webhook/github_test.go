package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestGitHubVerifySignature(t *testing.T) {
	secret := []byte("my_github_secret")
	payload := []byte(`{"action":"opened","number":42}`)

	// Compute valid signature.
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	p := &githubProvider{}

	tests := []struct {
		name    string
		params  SignatureParams
		wantErr bool
	}{
		{
			name: "valid signature",
			params: SignatureParams{
				Header:  validSig,
				Secret:  secret,
				Payload: payload,
			},
			wantErr: false,
		},
		{
			name: "missing header",
			params: SignatureParams{
				Header:  "",
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "empty secret",
			params: SignatureParams{
				Header:  validSig,
				Secret:  nil,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "wrong signature",
			params: SignatureParams{
				Header:  "sha256=wrongsig",
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "missing sha256 prefix",
			params: SignatureParams{
				Header:  hex.EncodeToString(mac.Sum(nil)),
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.VerifySignature(tc.params)
			if (err != nil) != tc.wantErr {
				t.Errorf("VerifySignature() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestGitHubParseEvent(t *testing.T) {
	payload := []byte(`{"action":"opened","number":42,"repository":{"full_name":"owner/repo"}}`)

	tests := []struct {
		name       string
		headers    map[string]string
		wantType   string
		wantAction string
	}{
		{
			name: "push event",
			headers: map[string]string{
				"X-GitHub-Event": "push",
			},
			wantType:   "push",
			wantAction: "opened",
		},
		{
			name: "pull_request event lowercase header",
			headers: map[string]string{
				"x-github-event": "pull_request",
			},
			wantType:   "pull_request",
			wantAction: "opened",
		},
		{
			name:       "no event header",
			headers:    map[string]string{},
			wantType:   "",
			wantAction: "opened",
		},
	}

	p := &githubProvider{}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event, err := p.ParseEvent(payload, tc.headers)
			if err != nil {
				t.Fatalf("ParseEvent() error = %v", err)
			}

			if event.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", event.Type, tc.wantType)
			}
			if action, ok := event.Payload["action"].(string); !ok || action != tc.wantAction {
				t.Errorf("Payload action = %v, want %q", event.Payload["action"], tc.wantAction)
			}
		})
	}
}
