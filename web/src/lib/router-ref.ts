import type { AnyRouter } from "@tanstack/react-router";

// The standalone navigate() helper needs the router instance, but importing the
// router module directly from the nav primitives would create a cycle
// (router -> pages -> useNavigate -> router). This holder is imported by both
// sides and references no app modules, so there's no eval-time cycle.
let registered: AnyRouter | null = null;

export function registerRouter(router: AnyRouter) {
  registered = router;
}

export function getRouter(): AnyRouter {
  if (!registered) throw new Error("router accessed before registration");
  return registered;
}
