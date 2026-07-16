import { createTool } from "./client";

type CreateToolBody = Parameters<typeof createTool>[0];

const objectSchemaBody: CreateToolBody = {
  namespace: "mock",
  name: "write",
  displayName: "Mock Write",
  description: "Requires approval",
  operationType: "write",
  riskLevel: "low",
  requiresApproval: true,
  inputSchemaJson: { type: "object" },
  outputSchemaJson: { type: "object" },
  enabled: true,
};
void objectSchemaBody;

const stringSchemaBody: CreateToolBody = {
  namespace: "mock",
  name: "write",
  displayName: "Mock Write",
  description: "Requires approval",
  operationType: "write",
  riskLevel: "low",
  requiresApproval: true,
  // @ts-expect-error schema JSON must be parsed before calling createTool.
  inputSchemaJson: "{\"type\":\"object\"}",
  // @ts-expect-error schema JSON must be parsed before calling createTool.
  outputSchemaJson: "{\"type\":\"object\"}",
  enabled: true,
};
void stringSchemaBody;
