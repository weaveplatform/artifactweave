# tflint for every Terraform root (deploy/check.sh, decision 0030). The cloud
# rulesets are pinned here; deploy/check.sh pins tflint itself. Deep checking
# stays off: it calls the cloud APIs and would need credentials.

config {
  call_module_type = "none"
}

plugin "terraform" {
  enabled = true
  preset  = "recommended"
}

plugin "aws" {
  enabled = true
  version = "0.49.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}

plugin "google" {
  enabled = true
  version = "0.40.0"
  source  = "github.com/terraform-linters/tflint-ruleset-google"
}

plugin "azurerm" {
  enabled = true
  version = "0.32.0"
  source  = "github.com/terraform-linters/tflint-ruleset-azurerm"
}

# Every root here is created and destroyed on purpose: the acceptance
# environments by make <cloud>-destroy, and the reference scenarios by the
# platform-scenarios destroy action (decision 0030). prevent_destroy would
# make each teardown fail.
rule "azurerm_resources_missing_prevent_destroy" {
  enabled = false
}
