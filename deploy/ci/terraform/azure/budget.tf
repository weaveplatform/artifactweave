# A monthly budget on the lab subscription. It alerts; it does not cap spend.
# Front Door Premium (the azure/cdn scenario) bills a base fee by the hour,
# so the alert is the backstop for an instance someone forgot to destroy.
resource "azurerm_consumption_budget_subscription" "lab" {
  name            = "artifactweave-lab"
  subscription_id = "/subscriptions/${var.subscription_id}"
  amount          = var.budget_amount
  time_grain      = "Monthly"

  time_period {
    start_date = var.budget_start
  }

  notification {
    enabled        = true
    threshold      = 50
    operator       = "GreaterThan"
    threshold_type = "Actual"
    contact_roles  = ["Owner"]
  }

  notification {
    enabled        = true
    threshold      = 90
    operator       = "GreaterThan"
    threshold_type = "Actual"
    contact_roles  = ["Owner"]
  }

  notification {
    enabled        = true
    threshold      = 100
    operator       = "GreaterThanOrEqualTo"
    threshold_type = "Actual"
    contact_roles  = ["Owner"]
  }
}
