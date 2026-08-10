package objectstore

import "io"

type tosPutRequest struct {
	Bucket    string
	Key       string
	SHA256    string
	SizeBytes int64
	Body      io.Reader
}
