Feature: Audit Webhooks and Signatures
  In order to track configuration changes across the fleet
  As a security auditor
  I need webhooks to be dispatched with standard JSON payloads and valid HMAC signatures

  Scenario: Audit webhook payload contains standard fields and valid HMAC signature
    Given a configured webhook dispatcher with secret "secret123"
    When an audit event of type "temporary_override.created" is published
    Then the webhook payload must contain a valid HMAC-SHA256 signature
    And the webhook payload must include actor, target, timestamp, and action
