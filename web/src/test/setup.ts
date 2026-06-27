import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Unmount the React tree between tests so queries don't bleed across cases.
afterEach(cleanup);
