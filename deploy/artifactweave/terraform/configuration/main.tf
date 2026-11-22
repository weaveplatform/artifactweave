resource "artifactory_proxy" "this" {
  key               = var.proxy_key
  host              = var.proxy_host
  port              = var.proxy_port
  username          = var.proxy_username
  password          = var.proxy_password
  platform_default  = var.proxy_platform_default
  redirect_to_hosts = var.proxy_redirect_to_hosts
  services          = var.proxy_services
}

resource "artifactory_property_set" "this" {
  name    = var.property_set_name
  visible = var.property_set_visible
  dynamic "property" {
    for_each = var.properties
    content {
      name                     = property.key
      closed_predefined_values = property.value.closed_predefined_values
      multiple_choice          = property.value.multiple_choice
      dynamic "predefined_value" {
        for_each = property.value.predefined_values
        content {
          name          = predefined_value.key
          default_value = predefined_value.value
        }
      }
    }
  }
}
