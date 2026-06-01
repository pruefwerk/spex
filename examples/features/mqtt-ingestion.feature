@smoke @mqtt
Feature: MQTT ingestion

  Background:
    Given tenant "tenant-dev"
    And device "device-dev-1"

  @graphql
  Scenario Outline: MQTT reading reaches Redpanda and GraphQL
    When device "device-dev-1" publishes energy reading <value> as "<correlationId>"
    Then Redpanda contains reading "<correlationId>" with value <value>
    And GraphQL returns reading "<correlationId>" for device "device-dev-1" with value <value>

    Examples:
      | correlationId | value |
      | reading-1     | 42.5  |
      | reading-2     | 11.0  |
