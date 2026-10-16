# A CloudFront distribution in front of one modules/aws/storage bucket, which
# serves artifactweave's blobs only to URLs it signs (decision 0029):
# CloudFront checks a canned-policy signature from a trusted key group on
# every request, and reads the private bucket through origin access control.
# aws/cdn composes it; it holds no provider block. CloudFront is global, and
# the one regional resource, the certificate, names us-east-1 itself (the AWS
# provider's per-resource region), so no provider alias is needed
# (decision 0030).

locals {
  custom = var.domain_name != null

  # As the s3 backend normalises blob.s3.prefix, so the grant matches the
  # keys it writes.
  trimmed      = trimprefix(var.store_prefix, "/")
  store_prefix = local.trimmed == "" || endswith(local.trimmed, "/") ? local.trimmed : "${local.trimmed}/"

  origin_id = "blobs"

  # CloudFront may read the committed blobs and nothing else: not upload
  # parts, not anything else in the bucket, and only for this distribution.
  # S3 allows one policy per bucket, so the caller hands this statement to
  # modules/aws/storage's policy_statements.
  statement = {
    Sid       = "CloudFrontReadsBlobs"
    Effect    = "Allow"
    Principal = { Service = "cloudfront.amazonaws.com" }
    Action    = "s3:GetObject"
    Resource  = "${var.origin_bucket.arn}/${local.store_prefix}sha256/*"
    Condition = { StringEquals = { "AWS:SourceArn" = aws_cloudfront_distribution.cdn.arn } }
  }
}

# CloudFront signs every request to the bucket with SigV4, so the bucket
# stays private and S3 still applies the response overrides, which it honours
# only on authenticated requests.
resource "aws_cloudfront_origin_access_control" "blobs" {
  name                              = var.name
  description                       = "Reads the artifactweave blobs in ${var.origin_bucket.bucket}."
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# The cache key is the path plus the two response overrides artifactweave
# signs into each URL. Without the overrides, one item would be served with
# another's filename; with the signature parameters, every URL would miss
# (decision 0029). CloudFront forwards what the key includes to the origin,
# so no origin request policy is needed, and it strips Expires, Key-Pair-Id,
# Policy and Signature itself. Blobs are content-addressed and never change,
# so an edge copy can live for a year; the signature is checked on every
# request, cached or not.
resource "aws_cloudfront_cache_policy" "blobs" {
  name        = var.name
  comment     = "artifactweave blobs: the path and the two response overrides."
  min_ttl     = 0
  default_ttl = 86400
  max_ttl     = 31536000

  parameters_in_cache_key_and_forwarded_to_origin {
    enable_accept_encoding_brotli = false
    enable_accept_encoding_gzip   = false

    cookies_config {
      cookie_behavior = "none"
    }
    headers_config {
      header_behavior = "none"
    }
    query_strings_config {
      query_string_behavior = "whitelist"
      query_strings {
        items = ["response-content-disposition", "response-content-type"]
      }
    }
  }
}

# A name prefix, so a replaced key can be created before the old one goes
# (CloudFront refuses to delete a key a key group still holds).
resource "aws_cloudfront_public_key" "signers" {
  for_each = var.public_keys

  name_prefix = "${var.name}-${each.key}-"
  comment     = "Signs artifactweave download URLs (${each.key})."
  encoded_key = each.value

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_cloudfront_key_group" "signers" {
  name    = var.name
  comment = "The keys artifactweave signs download URLs with."
  items   = [for k in aws_cloudfront_public_key.signers : k.id]
}

# CloudFront takes certificates from us-east-1 only.
resource "aws_acm_certificate" "cdn" {
  count = local.custom ? 1 : 0

  region            = "us-east-1"
  domain_name       = var.domain_name
  validation_method = "DNS"
  tags              = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

# One host has one validation record. ACM gives the same record to every
# certificate for that host in the account, so a record left by an earlier
# certificate is taken over rather than refused.
resource "aws_route53_record" "validation" {
  count = local.custom ? 1 : 0

  zone_id         = var.zone_id
  name            = one(aws_acm_certificate.cdn[0].domain_validation_options).resource_record_name
  type            = one(aws_acm_certificate.cdn[0].domain_validation_options).resource_record_type
  records         = [one(aws_acm_certificate.cdn[0].domain_validation_options).resource_record_value]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "cdn" {
  count = local.custom ? 1 : 0

  region                  = "us-east-1"
  certificate_arn         = aws_acm_certificate.cdn[0].arn
  validation_record_fqdns = [aws_route53_record.validation[0].fqdn]

  # ACM usually issues within minutes once the record resolves; a zone that
  # is not delegated would otherwise hold the apply for the provider's 75.
  timeouts {
    create = "20m"
  }
}

# Accepted trivy findings (deploy/check.sh), each with its reason:
# - AWS-0010: no access logs. Standard logging needs a bucket with ACLs on,
#   which the storage baseline turns off, and artifactweave counts every
#   download it redirects here.
# - AWS-0011: no WAF. Every request needs a valid signature, which CloudFront
#   checks before anything reaches the bucket, and a web ACL is billed
#   monthly.
#trivy:ignore:AWS-0010
#trivy:ignore:AWS-0011
resource "aws_cloudfront_distribution" "cdn" {
  enabled         = true
  comment         = "artifactweave blobs from ${var.origin_bucket.bucket}"
  is_ipv6_enabled = true
  http_version    = "http2and3"
  price_class     = var.price_class
  aliases         = local.custom ? [var.domain_name] : []
  tags            = var.tags

  # The bucket's root, with an empty origin path, so a URL's path is the
  # object key and artifactweave needs no path_prefix.
  origin {
    origin_id                = local.origin_id
    domain_name              = "${var.origin_bucket.bucket}.s3.${var.origin_bucket.region}.amazonaws.com"
    origin_path              = ""
    origin_access_control_id = aws_cloudfront_origin_access_control.blobs.id
  }

  # Only signed URLs, over HTTPS, for GET and HEAD. Compression is off, so
  # the edge serves each blob byte for byte and ranged requests keep their
  # offsets.
  default_cache_behavior {
    target_origin_id       = local.origin_id
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    viewer_protocol_policy = "https-only"
    cache_policy_id        = aws_cloudfront_cache_policy.blobs.id
    trusted_key_groups     = [aws_cloudfront_key_group.signers.id]
    compress               = false
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  # The *.cloudfront.net certificate always allows TLS 1.0, and CloudFront
  # reports TLSv1 for it whatever is asked, so asking for more would plan a
  # change on every run. A host of your own gets TLS 1.2 at least.
  viewer_certificate {
    cloudfront_default_certificate = !local.custom
    acm_certificate_arn            = local.custom ? aws_acm_certificate_validation.cdn[0].certificate_arn : null
    ssl_support_method             = local.custom ? "sni-only" : null
    minimum_protocol_version       = local.custom ? "TLSv1.2_2021" : "TLSv1"
  }
}

resource "aws_route53_record" "alias" {
  for_each = local.custom ? toset(["A", "AAAA"]) : toset([])

  zone_id = var.zone_id
  name    = var.domain_name
  type    = each.key

  alias {
    name                   = aws_cloudfront_distribution.cdn.domain_name
    zone_id                = aws_cloudfront_distribution.cdn.hosted_zone_id
    evaluate_target_health = false
  }
}
