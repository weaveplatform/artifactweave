output "server_provider" {
  value       = platform_reverse_proxy.this.server_provider
  description = "Web server type the reverse proxy settings describe."
}

output "public_server_name" {
  value       = platform_reverse_proxy.this.public_server_name
  description = "Host name clients use to reach the web server."
}
