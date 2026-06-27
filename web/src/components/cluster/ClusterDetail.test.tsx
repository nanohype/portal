import { describe, it, expect, beforeEach, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithClient } from "@/test/render";
import { useAuthStore } from "@/stores/auth";
import type { User } from "@/api/types";
import { ClusterDetail } from "./ClusterDetail";

// The api client binds globalThis.fetch at import, which makes network-level
// interception brittle; mock it at the module boundary instead. navigate()
// reaches the real router ref and confirm() comes from a provider we don't
// mount — drive both directly.
const { apiMock, navigateMock, confirmMock } = vi.hoisted(() => ({
  apiMock: { GET: vi.fn(), POST: vi.fn(), PUT: vi.fn(), DELETE: vi.fn() },
  navigateMock: vi.fn(),
  confirmMock: vi.fn(),
}));
vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/hooks/useNavigate", () => ({
  navigate: (to: string) => navigateMock(to),
  useLocation: () => "/",
}));
vi.mock("@/components/ui/confirm-context", () => ({
  useConfirm: () => confirmMock,
}));

const CLUSTER = {
  id: "c1",
  name: "prod-eks",
  description: "",
  account_id: "acct-1",
  api_endpoint: "https://eks.example",
  region: "us-west-2",
  connection_status: "connected",
  node_count: 3,
};

function setRole(role: string) {
  useAuthStore.setState({
    token: "test-token",
    user: { id: "u1", role } as User,
    isAuthenticated: true,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  apiMock.GET.mockResolvedValue({ data: CLUSTER, error: undefined });
  apiMock.DELETE.mockResolvedValue({ error: undefined });
});

describe("ClusterDetail RBAC", () => {
  it("hides destructive actions and locks the fields for a viewer", async () => {
    setRole("viewer");
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole("heading", { name: "prod-eks" });
    expect(screen.queryByRole("button", { name: /delete/i })).toBeNull();
    expect(
      screen.queryByRole("button", { name: /test connection/i }),
    ).toBeNull();
    expect(screen.getByDisplayValue("https://eks.example")).toBeDisabled();
  });

  it("exposes the actions and enables the fields for an admin", async () => {
    setRole("admin");
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole("heading", { name: "prod-eks" });
    expect(screen.getByRole("button", { name: /delete/i })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /test connection/i }),
    ).toBeInTheDocument();
    expect(screen.getByDisplayValue("https://eks.example")).toBeEnabled();
  });

  it("deletes (and navigates away) only after confirm resolves true", async () => {
    setRole("admin");
    confirmMock.mockResolvedValue(true);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole("heading", { name: "prod-eks" });
    await userEvent.click(screen.getByRole("button", { name: /delete/i }));

    expect(confirmMock).toHaveBeenCalledOnce();
    await waitFor(() => expect(apiMock.DELETE).toHaveBeenCalledOnce());
    await waitFor(() => expect(navigateMock).toHaveBeenCalledWith("/clusters"));
  });

  it("does not delete when confirm is dismissed", async () => {
    setRole("admin");
    confirmMock.mockResolvedValue(false);
    renderWithClient(<ClusterDetail clusterId="c1" />);

    await screen.findByRole("heading", { name: "prod-eks" });
    await userEvent.click(screen.getByRole("button", { name: /delete/i }));

    expect(confirmMock).toHaveBeenCalledOnce();
    // Let any (incorrect) mutation flush before asserting it never happened.
    await Promise.resolve();
    expect(apiMock.DELETE).not.toHaveBeenCalled();
    expect(navigateMock).not.toHaveBeenCalled();
  });
});
