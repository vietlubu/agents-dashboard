import { createRouter, createWebHashHistory } from "vue-router";

/**
 * Hash history: the frontend is served from embedded assets in a webview or from a plain
 * static file server, neither of which can be configured to rewrite unknown paths to
 * index.html.
 */
export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: "/", name: "overview", component: () => import("@/views/OverviewView.vue") },
    { path: "/realtime", name: "realtime", component: () => import("@/views/RealtimeView.vue") },
    { path: "/analysis", name: "analysis", component: () => import("@/views/AnalysisView.vue") },
    { path: "/models", name: "models", component: () => import("@/views/ModelsView.vue") },
    { path: "/events", name: "events", component: () => import("@/views/EventsView.vue") },
    { path: "/sessions", name: "sessions", component: () => import("@/views/SessionsView.vue") },
    { path: "/settings", name: "settings", component: () => import("@/views/SettingsView.vue") },
    { path: "/:pathMatch(.*)*", redirect: "/" },
  ],
});