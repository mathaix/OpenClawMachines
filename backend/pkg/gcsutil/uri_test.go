package gcsutil

import "testing"

func TestParseGCSURI(t *testing.T) {
	tests := []struct {
		name       string
		uri        string
		wantBucket string
		wantObject string
		wantErr    bool
	}{
		{
			name:       "valid simple path",
			uri:        "gs://mybucket/myobject",
			wantBucket: "mybucket",
			wantObject: "myobject",
		},
		{
			name:       "valid nested path",
			uri:        "gs://mybucket/path/to/object.json",
			wantBucket: "mybucket",
			wantObject: "path/to/object.json",
		},
		{
			name:       "valid deeply nested",
			uri:        "gs://openclawmachines/agent/manifest-abc123.json",
			wantBucket: "openclawmachines",
			wantObject: "agent/manifest-abc123.json",
		},
		{
			name:    "missing gs:// prefix",
			uri:     "s3://mybucket/myobject",
			wantErr: true,
		},
		{
			name:    "no prefix at all",
			uri:     "mybucket/myobject",
			wantErr: true,
		},
		{
			name:    "empty string",
			uri:     "",
			wantErr: true,
		},
		{
			name:    "bucket only no object",
			uri:     "gs://mybucket",
			wantErr: true,
		},
		{
			name:    "bucket with trailing slash",
			uri:     "gs://mybucket/",
			wantErr: true,
		},
		{
			name:    "empty bucket",
			uri:     "gs:///object",
			wantErr: true,
		},
		{
			name:    "gs:// only",
			uri:     "gs://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, object, err := ParseGCSURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseGCSURI(%q) error = %v, wantErr %v", tt.uri, err, tt.wantErr)
				return
			}
			if bucket != tt.wantBucket {
				t.Errorf("ParseGCSURI(%q) bucket = %q, want %q", tt.uri, bucket, tt.wantBucket)
			}
			if object != tt.wantObject {
				t.Errorf("ParseGCSURI(%q) object = %q, want %q", tt.uri, object, tt.wantObject)
			}
		})
	}
}
