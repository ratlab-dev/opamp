Feature: Temporary Redis Overrides and TTL Expiry
  In order to perform operational debugging and emergency adjustments
  As an OpAMP operator
  I need to apply temporary overrides via Redis with enforced TTL that safely expire or can be revoked

  Scenario: Temporary override takes precedence over GitOps
    Given an existing effective config with interval "30s"
    When an operator sets a temporary override for "prod-db-01" with interval "1s" and TTL "6h"
    Then the effective config for "prod-db-01" should have interval "1s"

  Scenario: Temporary override expires after TTL
    Given a temporary override for "prod-db-01" with interval "1s" and TTL "1ms"
    When the override TTL has expired
    Then the effective config for "prod-db-01" should revert to base interval "10s"

  Scenario: Operator revokes temporary override early
    Given an active temporary override for "prod-db-01" with interval "1s"
    When the operator revokes the temporary override for "prod-db-01"
    Then the effective config for "prod-db-01" should revert to base interval "10s"
