import { createI18n } from "vue-i18n";
import en from "./en.json";
import vi from "./vi.json";

/**
 * Locales. English is the default; Vietnamese is included because the dashboard is used on a
 * Vietnamese machine, and the two files are the whole translation surface.
 */
export const i18n = createI18n({
  legacy: false,
  locale: "en",
  fallbackLocale: "en",
  messages: { en, vi },
});

export type MessageSchema = typeof en;