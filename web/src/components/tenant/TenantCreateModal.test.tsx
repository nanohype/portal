import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { UserEvent } from "@testing-library/user-event";
import { renderWithClient } from "@/test/render";
import { useAuthStore } from "@/stores/auth";
import type { Cluster, User } from "@/api/models";
import { TenantCreateModal } from "./TenantCreateModal";

// Mock the api client at the module boundary (openapi-fetch binds fetch at
// import, so network-level interception is brittle).
const { apiMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
}));
vi.mock("@/api/client", () => ({ api: apiMock }));

const TEMPLATE = {
  id: "tpl1",
  name: "starter",
  description: "",
  persona: "eng",
  max_budget_usd: 1000,
  allowed_overrides: ["budget.monthlyUsd", "platform.persona"],
  allowed_model_families: [],
  required_compliance: [],
  default_values: { budget: { monthlyUsd: 500 }, platform: { persona: "eng" } },
};

const CLUSTER = {
  id: "cl1",
  name: "prod-eks",
  region: "us-west-2",
  connection_status: "connected",
} as unknown as Cluster;

function list<T>(items: T[]) {
  return { data: { data: items, total: items.length, page: 1, per_page: 50 } };
}

// Open the custom Select and click the named option. The trigger is a
// role="combobox" button whose accessible name doesn't come from its text, so
// target it by the displayed value text and walk up to the combobox. findBy so
// we wait out async-loaded Selects (Template renders after its query resolves).
async function choose(user: UserEvent, current: RegExp, option: RegExp) {
  const trigger = (await screen.findByText(current)).closest(
    '[role="combobox"]',
  );
  if (!trigger) throw new Error(`no combobox showing ${current}`);
  await user.click(trigger);
  await user.click(await screen.findByRole("option", { name: option }));
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({
    token: "test-token",
    user: { id: "u1", role: "admin" } as User,
    isAuthenticated: true,
  });
  apiMock.GET.mockImplementation((path: string) => {
    if (path === "/templates") return Promise.resolve(list([TEMPLATE]));
    if (path === "/teams") return Promise.resolve(list([]));
    return Promise.resolve({ error: { message: `unexpected GET ${path}` } });
  });
});

describe("TenantCreateModal canSubmit gating", () => {
  it("enables Create only once a cluster, template, and valid name are set", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />,
    );

    const create = await screen.findByRole("button", { name: /create tenant/i });
    expect(create).toBeDisabled();

    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText("marketing-team"), "eng-team");

    await waitFor(() => expect(create).toBeEnabled());
  });

  it("keeps Create disabled for an invalid k8s name", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />,
    );

    await screen.findByRole("button", { name: /create tenant/i });
    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText("marketing-team"), "Bad_Name");

    expect(screen.getByRole("button", { name: /create tenant/i })).toBeDisabled();
  });

  it("keeps Create disabled when the budget exceeds the template cap", async () => {
    const user = userEvent.setup();
    renderWithClient(
      <TenantCreateModal open onClose={vi.fn()} clusters={[CLUSTER]} />,
    );

    await screen.findByRole("button", { name: /create tenant/i });
    await choose(user, /pick a cluster/i, /prod-eks/i);
    await choose(user, /pick a template/i, /starter/i);
    await user.type(screen.getByPlaceholderText("marketing-team"), "eng-team");

    // Over the template's $1000 cap.
    const budget = screen.getByRole("spinbutton");
    await user.clear(budget);
    await user.type(budget, "2000");

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /create tenant/i })).toBeDisabled(),
    );
  });
});
