# Resources that exist before this root: adopted, not recreated.
#
# - The hosted zone was created by CLI so its name servers could be delegated
#   at GoDaddy before this root existed.

import {
  to = aws_route53_zone.aws
  id = var.dns_zone_id
}
