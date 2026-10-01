// Application-level API that does not belong to a page.
import * as App from "@bindings/github.com/vietlubu/agents-dashboard/internal/service/appservice";

/** Startup warnings (for example an unusable timezone) that would otherwise only reach the log. */
export const warnings = App.Warnings as () => Promise<string[]>;

/** The build version reported by the binary. */
export const version = App.Version as () => Promise<string>;