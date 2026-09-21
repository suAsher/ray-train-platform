package registryauth

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
 "sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	registrytransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/validate"
)

var ErrArtifact = errors.New("environment OCI artifact is invalid or incomplete")
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type PublishRequest struct{ LayoutPath, Digest, Project, Repository, Tag string }
type PublishResult struct {
	ImageDigest string `json:"imageDigest,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
}

// PublishDiagnostic contains only bounded protocol categories. Never add URLs,
// headers, response bodies or an underlying error's text to this event.
type PublishDiagnostic struct {
 Stage string `json:"stage,omitempty"`
 Code string `json:"code,omitempty"`
 Method string `json:"method,omitempty"`
 Status int `json:"status,omitempty"`
 Timeout bool `json:"timeout,omitempty"`
}

type publishDiagnosticKey struct{}
type publishDiagnosticSink struct {
 mutex sync.Mutex
 receive func(PublishDiagnostic)
}

// WithPublishDiagnostics installs a serialized callback for the publisher's
// allowlisted progress events, including concurrent layer transfer events.
func WithPublishDiagnostics(ctx context.Context, receive func(PublishDiagnostic)) context.Context {
 if receive == nil { return ctx }
 return context.WithValue(ctx, publishDiagnosticKey{}, &publishDiagnosticSink{receive:receive})
}

func publishDiagnostic(ctx context.Context, event PublishDiagnostic) {
 sink, _ := ctx.Value(publishDiagnosticKey{}).(*publishDiagnosticSink)
 if sink == nil { return }
 sink.mutex.Lock()
 defer sink.mutex.Unlock()
 sink.receive(event)
}

// Publish never executes the image or artifact contents. The controller must
// mount the dedicated OCI artifact volume read-only after the build has exited.
func (c *Client) Publish(ctx context.Context, credentials Credentials, request PublishRequest) (PublishResult, error) {
	target, err := ValidateTarget(request.Project, request.Repository)
	if err != nil || !tagPattern.MatchString(request.Tag) {
		return PublishResult{}, ErrInvalidTarget
	}
	publishDiagnostic(ctx, PublishDiagnostic{Stage:"LAYOUT_VALIDATING"})
 img, err := loadPublishImage(ctx, request.LayoutPath, request.Digest)
	if err != nil {
		return PublishResult{}, err
	}
	publishDiagnostic(ctx, PublishDiagnostic{Stage:"REGISTRY_AUTH"})
 token, err := c.pushToken(ctx, credentials, target.Repository)
	if err != nil {
		return PublishResult{}, err
	}
	reference, err := name.NewTag(Host+"/"+target.Repository+":"+request.Tag, name.StrictValidation)
	if err != nil {
		return PublishResult{}, ErrInvalidTarget
	}
	transport := &publishTransport{base: c.http.Transport, repository: target.Repository}
	options := []remote.Option{remote.WithContext(ctx), remote.WithAuth(&authn.Bearer{Token: token}), remote.WithTransport(transport), remote.WithJobs(2)}
	publishDiagnostic(ctx, PublishDiagnostic{Stage:"REGISTRY_WRITE"})
 if err := remote.Write(reference, img, options...); err != nil {
		return PublishResult{}, publishError(err)
	}
	publishDiagnostic(ctx, PublishDiagnostic{Stage:"REGISTRY_VERIFY"})
 descriptor, err := remote.Head(reference, options...)
	if err != nil {
		return PublishResult{}, publishError(err)
	}
	if descriptor.Digest.String() != request.Digest {
		return PublishResult{}, ErrArtifact
	}
	return PublishResult{ImageDigest: request.Digest}, nil
}

func publishError(err error) error {
	var remoteError *registrytransport.Error
	// Underlying error bodies and token-bearing upload URLs must never escape.
	if errors.As(err, &remoteError) {
		switch remoteError.StatusCode {
		case http.StatusUnauthorized:
			return ErrCredentials
		case http.StatusForbidden:
			return ErrForbidden
		}
	}
	return ErrUnavailable
}

func safeLayout(ctx context.Context, directory string) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrArtifact
	}
	count := 0
	return filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ErrArtifact
		}
		if err != nil {
			return ErrArtifact
		}
		count++
		if count > 100000 {
			return ErrArtifact
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrArtifact
		}
		return nil
	})
}

// Validate compressed blob hashes without extracting layers onto the filesystem.
// validate.Fast still checks manifest/config invariants; layer content integrity
// is checked below with bounded streaming reads against manifest descriptors.
func loadPublishImage(ctx context.Context, directory, digest string) (v1.Image, error) {
	if !digestPattern.MatchString(digest) || safeLayout(ctx, directory) != nil {
		return nil, ErrArtifact
	}
	if !boundedFile(filepath.Join(directory, "index.json"), 2<<20) || !boundedFile(filepath.Join(directory, "blobs", "sha256", digest[7:]), 2<<20) {
		return nil, ErrArtifact
	}
	path, err := layout.FromPath(directory)
	if err != nil {
		return nil, ErrArtifact
	}
	hash, err := v1.NewHash(digest)
	if err != nil {
		return nil, ErrArtifact
	}
	img, err := path.Image(hash)
	if err != nil {
		return nil, ErrArtifact
	}
	manifest, err := img.Manifest()
	if err != nil || len(manifest.Config.URLs) > 0 || len(manifest.Layers) > 4096 {
		return nil, ErrArtifact
	}
	for _, layer := range manifest.Layers {
		if len(layer.URLs) > 0 || !digestPattern.MatchString(layer.Digest.String()) || layer.Size < 0 || layer.Size > 80<<30 {
			return nil, ErrArtifact
		}
	}
	if !digestPattern.MatchString(manifest.Config.Digest.String()) || manifest.Config.Size < 0 || manifest.Config.Size > 16<<20 || !boundedFile(filepath.Join(directory, "blobs", "sha256", manifest.Config.Digest.Hex), 16<<20) {
		return nil, ErrArtifact
	}
	if validate.Image(img, validate.Fast) != nil {
		return nil, ErrArtifact
	}
	actual, err := img.Digest()
	if err != nil || actual.String() != digest {
		return nil, ErrArtifact
	}
	for _, descriptor := range manifest.Layers {
		if ctx.Err() != nil {
			return nil, ErrArtifact
		}
		layer, err := img.LayerByDigest(descriptor.Digest)
		if err != nil {
			return nil, ErrArtifact
		}
		reader, err := layer.Compressed()
		if err != nil {
			return nil, ErrArtifact
		}
		actual, size, readErr := v1.SHA256(io.LimitReader(contextReader{ctx: ctx, reader: reader}, descriptor.Size+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || actual != descriptor.Digest || size != descriptor.Size {
			return nil, ErrArtifact
		}
	}
	return img, nil
}

func boundedFile(path string, limit int64) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() <= limit
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
