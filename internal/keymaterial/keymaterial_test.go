package keymaterial

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestDecode(t *testing.T) {
	validHex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	validHexBytes := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}
	validBase64Bytes := bytes.Repeat([]byte{0x42}, 32)
	validBase64 := base64.StdEncoding.EncodeToString(validBase64Bytes)
	ambiguous := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefab"
	ambiguousHexBytes := []byte{
		0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd,
		0xef, 0xab, 0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab,
		0xcd, 0xef, 0xab, 0xcd, 0xef, 0xab,
	}

	tests := []struct {
		name         string
		raw          string
		minimumBytes int
		want         []byte
		wantErr      string
	}{
		{
			name:         "valid hex",
			raw:          validHex,
			minimumBytes: 32,
			want:         validHexBytes,
		},
		{
			name:         "valid base64",
			raw:          validBase64,
			minimumBytes: 32,
			want:         validBase64Bytes,
		},
		{
			name:         "ambiguous input is deterministically hex",
			raw:          ambiguous,
			minimumBytes: 16,
			want:         ambiguousHexBytes,
		},
		{
			name:         "minimum length does not select base64 bytes",
			raw:          ambiguous,
			minimumBytes: 32,
			wantErr:      "TEST_KEY must decode to at least 32 bytes",
		},
		{
			name:         "too short hex",
			raw:          "0102",
			minimumBytes: 32,
			wantErr:      "TEST_KEY must decode to at least 32 bytes",
		},
		{
			name:         "too short base64",
			raw:          "YWI=",
			minimumBytes: 32,
			wantErr:      "TEST_KEY must decode to at least 32 bytes",
		},
		{
			name:         "malformed",
			raw:          "not-hex-or-base64-!@#$%",
			minimumBytes: 32,
			wantErr:      "TEST_KEY must be hex or base64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode("TEST_KEY", tt.raw, tt.minimumBytes)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("expected error %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("Decode() = %x, want %x", got, tt.want)
			}
		})
	}
}
