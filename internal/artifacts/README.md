# Object storage contract

Set `KUBERNETES_ARTIFACT_BUCKET` and `KUBERNETES_ARTIFACT_REGION` on the trusted control plane. `KUBERNETES_ARTIFACT_PREFIX` isolates an installation within a private bucket. For an S3-compatible service, set `KUBERNETES_ARTIFACT_ENDPOINT` and, when required, `KUBERNETES_ARTIFACT_PATH_STYLE=true`. The AWS default credential chain is used unless `KUBERNETES_ARTIFACT_ACCESS_KEY_ID` and `KUBERNETES_ARTIFACT_SECRET_ACCESS_KEY` are both set; `KUBERNETES_ARTIFACT_SESSION_TOKEN` is optional. Runner Pods must not receive these credentials.

The bucket principal needs `s3:PutObject`, `s3:GetObject`, `s3:ListBucket`, and `s3:DeleteObject` within the configured prefix. Put uses [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html) (`If-None-Match: *`); a bucket policy should require that condition. Object names are `<prefix>/objects/<task>/<execution>/<kind>/<name>`. The control plane records a SHA-256 and size in each immutable reference and verifies both when reading.

`DeleteTask` removes current objects after the caller has confirmed Runner termination and report persistence. In a versioned bucket, [DeleteObject creates a delete marker](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html); configure noncurrent-version expiration or use an unversioned artifact bucket to enforce the retention period.

The isolated live test uses `SWARMJET_TEST_S3_ENDPOINT`, `SWARMJET_TEST_S3_ACCESS_KEY`, and `SWARMJET_TEST_S3_SECRET_KEY` with a disposable object service. It creates and deletes its own bucket; ordinary test runs skip it.
