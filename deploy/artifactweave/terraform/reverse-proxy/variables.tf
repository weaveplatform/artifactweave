variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "server_provider" {
  type        = string
  default     = "NGINX"
  description = "Web server in front of artifactweave: NGINX, APACHE or DIRECT. Only NGINX has a configuration snippet."
}

variable "docker_reverse_proxy_method" {
  type        = string
  default     = "SUBDOMAIN"
  description = "How Docker clients address a repository: SUBDOMAIN (<repo>.<public_server_name>) or REPOPATHPREFIX (/v2/<repo>/...). artifactweave offers no PORTPERREPO."
}

variable "public_server_name" {
  type        = string
  description = "Host name clients use to reach the web server; keep it in step with the base URL."
}

variable "internal_hostname" {
  type        = string
  default     = "localhost"
  description = "Host name or IP address the web server uses to reach artifactweave."
}

variable "use_https" {
  type        = bool
  default     = false
  description = "Whether the web server also listens for HTTPS; needs ssl_key_path and ssl_certificate_path."
}

variable "http_port" {
  type        = number
  default     = 80
  description = "Port the web server listens on for HTTP."
}

variable "https_port" {
  type        = number
  default     = 443
  description = "Port the web server listens on for HTTPS when use_https is true."
}

variable "ssl_key_path" {
  type        = string
  default     = null
  description = "Absolute path of the TLS key file on the web server host. A path, not key material."
}

variable "ssl_certificate_path" {
  type        = string
  default     = null
  description = "Absolute path of the TLS certificate file on the web server host."
}
