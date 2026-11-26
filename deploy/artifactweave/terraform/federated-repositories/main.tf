# Each resource has three members. Provider member access tokens authenticate
# cleanup_on_delete requests; the provider keeps them out of repository JSON.

resource "artifactory_federated_alpine_repository" "this" {
  key               = "${var.name_prefix}-alpine"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-alpine"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_ansible_repository" "this" {
  key               = "${var.name_prefix}-ansible"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-ansible"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_bower_repository" "this" {
  key               = "${var.name_prefix}-bower"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-bower"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_cargo_repository" "this" {
  key               = "${var.name_prefix}-cargo"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-cargo"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_chef_repository" "this" {
  key               = "${var.name_prefix}-chef"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-chef"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_cocoapods_repository" "this" {
  key               = "${var.name_prefix}-cocoapods"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-cocoapods"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_composer_repository" "this" {
  key               = "${var.name_prefix}-composer"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-composer"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_conan_repository" "this" {
  key               = "${var.name_prefix}-conan"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-conan"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_conda_repository" "this" {
  key               = "${var.name_prefix}-conda"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-conda"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_cran_repository" "this" {
  key               = "${var.name_prefix}-cran"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-cran"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_debian_repository" "this" {
  key               = "${var.name_prefix}-debian"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-debian"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_docker_repository" "this" {
  key               = "${var.name_prefix}-docker"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-docker"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_docker_v2_repository" "this" {
  key               = "${var.name_prefix}-docker-v2"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-docker-v2"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_gems_repository" "this" {
  key               = "${var.name_prefix}-gems"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-gems"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_generic_repository" "this" {
  key               = "${var.name_prefix}-generic"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-generic"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_gitlfs_repository" "this" {
  key               = "${var.name_prefix}-gitlfs"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-gitlfs"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_go_repository" "this" {
  key               = "${var.name_prefix}-go"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-go"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_gradle_repository" "this" {
  key               = "${var.name_prefix}-gradle"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-gradle"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_helm_repository" "this" {
  key               = "${var.name_prefix}-helm"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-helm"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_helmoci_repository" "this" {
  key               = "${var.name_prefix}-helmoci"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-helmoci"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_huggingfaceml_repository" "this" {
  key               = "${var.name_prefix}-huggingfaceml"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-huggingfaceml"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_ivy_repository" "this" {
  key               = "${var.name_prefix}-ivy"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-ivy"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_maven_repository" "this" {
  key               = "${var.name_prefix}-maven"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-maven"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_npm_repository" "this" {
  key               = "${var.name_prefix}-npm"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-npm"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_nuget_repository" "this" {
  key               = "${var.name_prefix}-nuget"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-nuget"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_oci_repository" "this" {
  key               = "${var.name_prefix}-oci"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-oci"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_opkg_repository" "this" {
  key               = "${var.name_prefix}-opkg"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-opkg"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_puppet_repository" "this" {
  key               = "${var.name_prefix}-puppet"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-puppet"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_pypi_repository" "this" {
  key               = "${var.name_prefix}-pypi"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-pypi"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_rpm_repository" "this" {
  key               = "${var.name_prefix}-rpm"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-rpm"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_sbt_repository" "this" {
  key               = "${var.name_prefix}-sbt"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-sbt"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_swift_repository" "this" {
  key               = "${var.name_prefix}-swift"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-swift"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_terraform_module_repository" "this" {
  key               = "${var.name_prefix}-terraform-module"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-terraform-module"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_terraform_provider_repository" "this" {
  key               = "${var.name_prefix}-terraform-provider"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-terraform-provider"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

resource "artifactory_federated_vagrant_repository" "this" {
  key               = "${var.name_prefix}-vagrant"
  description       = var.description
  cleanup_on_delete = true

  dynamic "member" {
    for_each = var.member_urls
    content {
      url          = "${member.value}/artifactory/${var.name_prefix}-vagrant"
      enabled      = true
      access_token = lookup(var.member_access_tokens, member.value, null)
    }
  }
}

