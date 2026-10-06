package webhook

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestGenericVerifySignature(t *testing.T) {
	secret := []byte("my_generic_secret")
	payload := []byte(`{"event":"test","data":{"id":123}}`)

	// Compute valid SHA256 signature.
	mac256 := hmac.New(sha256.New, secret)
	mac256.Write(payload)
	validSig256 := hex.EncodeToString(mac256.Sum(nil))

	// Compute valid SHA1 signature.
	mac1 := hmac.New(sha1.New, secret)
	mac1.Write(payload)
	validSig1 := hex.EncodeToString(mac1.Sum(nil))

	p := &genericProvider{}

	tests := []struct {
		name    string
		params  SignatureParams
		wantErr bool
	}{
		{
			name: "valid sha256 raw hex",
			params: SignatureParams{
				Header:    validSig256,
				Algorithm: "sha256",
				Secret:    secret,
				Payload:   payload,
			},
			wantErr: false,
		},
		{
			name: "valid sha256 prefixed",
			params: SignatureParams{
				Header:  "sha256=" + validSig256,
				Secret:  secret,
				Payload: payload,
			},
			wantErr: false,
		},
		{
			name: "valid sha1 prefixed",
			params: SignatureParams{
				Header:  "sha1=" + validSig1,
				Secret:  secret,
				Payload: payload,
			},
			wantErr: false,
		},
		{
			name: "valid sha1 explicit algorithm",
			params: SignatureParams{
				Header:    validSig1,
				Algorithm: "sha1",
				Secret:    secret,
				Payload:   payload,
			},
			wantErr: false,
		},
		{
			name: "default to sha256",
			params: SignatureParams{
				Header:  validSig256,
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
				Header:  validSig256,
				Secret:  nil,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "wrong signature",
			params: SignatureParams{
				Header:  "wrongsig",
				Secret:  secret,
				Payload: payload,
			},
			wantErr: true,
		},
		{
			name: "unsupported algorithm",
			params: SignatureParams{
				Header:    validSig256,
				Algorithm: "md5",
				Secret:    secret,
				Payload:   payload,
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

func TestGenericParseEvent(t *testing.T) {
	p := &genericProvider{}

	tests := []struct {
		name       string
		payload    []byte
		wantType   string
		wantHasID  bool
	}{
		{
			name:      "payload with type field",
			payload:   []byte(`{"type":"order.created","data":{"id":"123"}}`),
			wantType:  "order.created",
			wantHasID: false,
		},
		{
			name:      "payload without type field",
			payload:   []byte(`{"event":"something","id":"456"}`),
			wantType:  "",
			wantHasID: true,
		},
		{
			name:      "empty payload",
			payload:   []byte(`{}`),
			wantType:  "",
			wantHasID: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event, err := p.ParseEvent(tc.payload, map[string]string{})
			if err != nil {
				t.Fatalf("ParseEvent() error = %v", err)
			}

			if event.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", event.Type, tc.wantType)
			}

			_, hasID := event.Payload["id"]
			if hasID != tc.wantHasID {
				t.Errorf("hasID = %v, want %v", hasID, tc.wantHasID)
			}
		})
	}
}
