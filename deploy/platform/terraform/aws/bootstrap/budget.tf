# A monthly cost budget on the lab account. It alerts; it does not cap spend.
# Scenario CDN resources carry charges while they exist, so the alert is the
# backstop for an instance someone forgot to destroy. Two budgets per account
# are free.
#
# The alert address is given once, when the budget is created, and lives in
# AWS and the private state from then on, never in this repository. Later
# plans and applies need no address: Terraform ignores the alerts it would
# otherwise remove. To change the address, edit the budget's alerts in the
# console, or apply once with BUDGET_EMAIL and -replace=aws_budgets_budget.lab.
resource "aws_budgets_budget" "lab" {
  name         = "artifactweave-lab"
  budget_type  = "COST"
  limit_amount = tostring(var.budget_amount)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  dynamic "notification" {
    for_each = var.budget_alert_email == null ? [] : [50, 90, 100]
    content {
      comparison_operator        = "GREATER_THAN"
      threshold                  = notification.value
      threshold_type             = "PERCENTAGE"
      notification_type          = "ACTUAL"
      subscriber_email_addresses = [var.budget_alert_email]
    }
  }

  lifecycle {
    ignore_changes = [notification]
  }
}
