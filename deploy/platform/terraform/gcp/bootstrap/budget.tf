# A monthly budget on the lab project. It alerts; it does not cap spend.
# Scenario CDN resources carry hourly charges (a load balancer's forwarding
# rule), so the alert is the backstop for an instance someone forgot to
# destroy. Alerts go to the billing account's administrators.
resource "google_billing_budget" "lab" {
  provider        = google.billing
  billing_account = var.billing_account
  display_name    = "artifactweave lab"

  budget_filter {
    projects = ["projects/${data.google_project.this.number}"]
  }

  amount {
    specified_amount {
      units = tostring(var.budget_amount)
    }
  }

  threshold_rules {
    threshold_percent = 0.5
  }
  threshold_rules {
    threshold_percent = 0.9
  }
  threshold_rules {
    threshold_percent = 1.0
  }

  depends_on = [google_project_service.this]
}
