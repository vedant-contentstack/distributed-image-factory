-- Cloud Spanner DDL for Distributed Image-Factory
-- Replace database within your project/instance via gcloud or API.
-- Example apply:
-- gcloud spanner databases ddl update YOUR_DB \
--   --instance=YOUR_INSTANCE \
--   --ddl-file=migrations/spanner.sql

-- Images table stores the original upload.
CREATE TABLE Images (
  ImageID STRING(MAX) NOT NULL,
  Original BYTES(MAX),
  OriginalExt STRING(16),
  CreatedAt TIMESTAMP OPTIONS (allow_commit_timestamp=true)
) PRIMARY KEY (ImageID);

-- Variants table stores transformed outputs for an image
-- (e.g., thumbnail, grayscale). Primary key allows 1 row per op.
CREATE TABLE Variants (
  ImageID STRING(MAX) NOT NULL,
  Op STRING(MAX) NOT NULL,
  Data BYTES(MAX),
  ContentType STRING(64),
  CreatedAt TIMESTAMP OPTIONS (allow_commit_timestamp=true)
) PRIMARY KEY (ImageID, Op);

-- Helpful index to list images by creation time (optional)
CREATE INDEX ImagesByCreatedAt ON Images (CreatedAt DESC);

-- Helpful index to list recent variants (optional)
CREATE INDEX VariantsByCreatedAt ON Variants (CreatedAt DESC);


