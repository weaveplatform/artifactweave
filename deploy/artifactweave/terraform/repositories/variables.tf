variable "url" {
  type        = string
  description = "Base URL of artifactweave, without /artifactory, for example https://artifacts.example.com."
}

variable "access_token" {
  type        = string
  description = "Admin access token, sent as a Bearer token."
  sensitive   = true
}

variable "repository_key" {
  type        = string
  default     = "generic-local"
  description = "Key of the local generic repository."
}

variable "item_path" {
  type        = string
  default     = "fixture.txt"
  description = "Path, inside the repository, of the file the properties are set on. The file must exist before the properties are applied: the provider cannot deploy content."
}

variable "properties" {
  type = map(list(string))
  default = {
    team = ["platform"]
    tier = ["gold", "silver"]
  }
  description = "Properties to set on item_path, each a key with its values."
}

variable "with_properties" {
  type        = bool
  default     = true
  description = "Whether to set the properties on item_path. Apply with false first, deploy the file, then apply with true."
}
