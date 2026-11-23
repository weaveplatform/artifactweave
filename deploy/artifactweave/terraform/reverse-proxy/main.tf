# The reverse proxy settings are a singleton: one per instance, replaced
# whole by every write. The provider's delete sends nothing and warns
# "Reverse proxy cannot be deleted", so terraform destroy leaves the last
# settings in place.
resource "platform_reverse_proxy" "this" {
  server_provider             = var.server_provider
  docker_reverse_proxy_method = var.docker_reverse_proxy_method
  public_server_name          = var.public_server_name
  internal_hostname           = var.internal_hostname
  use_https                   = var.use_https
  http_port                   = var.http_port
  https_port                  = var.https_port
  ssl_key_path                = var.ssl_key_path
  ssl_certificate_path        = var.ssl_certificate_path
}
