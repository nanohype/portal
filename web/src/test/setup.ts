import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom doesn't implement scrollIntoView, which the custom Select calls when it
// opens. No-op it so dropdown interactions work under test.
Element.prototype.scrollIntoView = () => {};

// Unmount the React tree between tests so queries don't bleed across cases.
afterEach(cleanup);
