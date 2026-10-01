<script setup lang="ts">
import { RouterLink } from "vue-router";

const items = [
  { to: "/", key: "nav.overview" },
  { to: "/realtime", key: "nav.realtime" },
  { to: "/analysis", key: "nav.analysis" },
  { to: "/events", key: "nav.events" },
  { to: "/sessions", key: "nav.sessions" },
  { to: "/settings", key: "nav.settings" },
];
</script>

<template>
  <aside class="sidebar">
    <div class="brand">
      <span class="brand-dot" />
      <span>agent-dashboard</span>
    </div>
    <nav>
      <RouterLink v-for="item in items" :key="item.to" :to="item.to" class="nav-link">
        {{ $t(item.key) }}
      </RouterLink>
    </nav>
    <div class="spacer" />
    <slot name="footer" />
  </aside>
</template>

<style scoped>
.sidebar {
  width: 190px;
  flex: 0 0 190px;
  background: var(--bg-sunken);
  border-right: 1px solid var(--border);
  padding: 14px 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 13px;
  letter-spacing: 0.01em;
}

.brand-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--accent);
}

nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.nav-link {
  padding: 6px 9px;
  border-radius: var(--radius-sm);
  color: var(--text-muted);
  font-size: 13px;
}

.nav-link:hover {
  background: var(--bg-elevated);
  color: var(--text);
}

.nav-link.router-link-active {
  background: var(--accent-soft);
  color: var(--text);
}

@media (max-width: 860px) {
  .sidebar {
    width: 100%;
    flex: 0 0 auto;
    border-right: 0;
    border-bottom: 1px solid var(--border);
  }

  nav {
    flex-direction: row;
    flex-wrap: wrap;
  }
}
</style>