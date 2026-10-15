package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// S3Config describes an existing private bucket. Empty credentials use the
// AWS default chain (including workload identity); explicit keys are intended
// for isolated S3-compatible endpoints. Endpoint is optional for AWS S3.
type S3Config struct {
	Bucket          string
	Region          string
	Prefix          string
	Endpoint        string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type s3Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3Store keeps the same logical keys and Ref format as FileStore. Configure
// the bucket to require conditional PutObject writes and to forbid Runner
// credentials. Retention uses DeleteTask after the control-plane cleanup gate.
type S3Store struct {
	client s3Client
	bucket string
	prefix string
}

func NewS3Store(ctx context.Context, c S3Config) (*S3Store, error) {
	if c.Bucket == "" || c.Region == "" || strings.Contains(c.Bucket, "/") || strings.Contains(c.Prefix, "..") || (c.AccessKeyID == "") != (c.SecretAccessKey == "") {
		return nil, errors.New("invalid S3 artifact configuration")
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(c.Region)}
	if c.AccessKeyID != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, c.SessionToken)))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.PathStyle
	})
	return &S3Store{client: client, bucket: c.Bucket, prefix: strings.Trim(c.Prefix, "/")}, nil
}

func (s *S3Store) key(r Request) string {
	name := "objects/" + r.TaskID + "/" + r.ExecutionID + "/" + string(r.Kind) + "/" + r.Name
	if s.prefix != "" {
		return s.prefix + "/" + name
	}
	return name
}

func (s *S3Store) taskPrefix(taskID string) string {
	name := "objects/" + taskID + "/"
	if s.prefix != "" {
		return s.prefix + "/" + name
	}
	return name
}

func (s *S3Store) Put(ctx context.Context, r Request, src io.Reader) (Ref, error) {
	if s == nil || s.client == nil || src == nil {
		return Ref{}, errors.New("S3 artifact store is not configured")
	}
	if err := validRequest(r); err != nil {
		return Ref{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	b, err := io.ReadAll(io.LimitReader(src, limit(r.Kind)+1))
	if err != nil {
		return Ref{}, err
	}
	if int64(len(b)) > limit(r.Kind) {
		return Ref{}, errors.New("artifact exceeds size limit")
	}
	if err = validate(r, b); err != nil {
		return Ref{}, err
	}
	sum := sha256.Sum256(b)
	ref := Ref{TaskID: r.TaskID, ExecutionID: r.ExecutionID, Kind: r.Kind, Name: r.Name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(r)), Body: bytes.NewReader(b),
		ContentLength: aws.Int64(int64(len(b))), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
		IfNoneMatch: aws.String("*"), ContentType: aws.String(contentType(r.Kind)),
	})
	if err == nil {
		return ref, nil
	}
	// 412 means a prior object exists; a timeout can also mean our write
	// succeeded. In both cases compare the actual bytes before deciding.
	if _, readErr := s.Read(ctx, ref); readErr == nil {
		return ref, nil
	}
	// A stored object at this key with different bytes must never be replaced.
	if _, readErr := s.readKey(ctx, r); readErr == nil {
		return Ref{}, ErrConflict
	}
	return Ref{}, err
}

func (s *S3Store) Read(ctx context.Context, ref Ref) ([]byte, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("S3 artifact store is not configured")
	}
	r := Request{TaskID: ref.TaskID, ExecutionID: ref.ExecutionID, Kind: ref.Kind, Name: ref.Name}
	if ref.Kind == Result {
		r.Role = "read"
	}
	if err := validRequest(r); err != nil || len(ref.SHA256) != 64 || ref.Size < 0 || ref.Size > limit(ref.Kind) {
		return nil, errors.New("invalid artifact reference")
	}
	if _, err := hex.DecodeString(ref.SHA256); err != nil {
		return nil, errors.New("invalid artifact digest")
	}
	b, err := s.readKey(ctx, r)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != ref.Size || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, errors.New("artifact digest mismatch")
	}
	return b, nil
}

func (s *S3Store) readKey(ctx context.Context, r Request) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(r))})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, limit(r.Kind)+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit(r.Kind) {
		return nil, errors.New("artifact exceeds size limit")
	}
	return b, nil
}

// DeleteTask removes only keys under one validated task prefix. For versioned
// buckets this removes current versions; old versions need a bucket lifecycle
// rule or separate version retirement.
func (s *S3Store) DeleteTask(ctx context.Context, taskID string) error {
	if s == nil || s.client == nil || !domain.IDPattern.MatchString(taskID) {
		return errors.New("invalid artifact task identity")
	}
	prefix := s.taskPrefix(taskID)
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			if object.Key == nil || !strings.HasPrefix(*object.Key, prefix) {
				return errors.New("S3 listed object outside task prefix")
			}
			if _, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: object.Key}); err != nil {
				return fmt.Errorf("delete artifact: %w", err)
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil
		}
		if page.NextContinuationToken == nil || *page.NextContinuationToken == "" {
			return errors.New("S3 truncated listing without continuation token")
		}
		token = page.NextContinuationToken
	}
}

func contentType(k Kind) string {
	if k == Result {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}

var _ Store = (*S3Store)(nil)
