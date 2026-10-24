// views/index.js is the registry of the views of the dashboard.

import { mount as mountLogin } from "./login.js";
import { mount as mountOverview } from "./overview.js";
import { mount as mountInstances } from "./instances.js";
import { mount as mountJobs } from "./jobs.js";
import { mount as mountArtifacts } from "./artifacts.js";
import { mount as mountTemplates } from "./templates.js";
import { mount as mountAdmin } from "./admin.js";

export const views = {
  login: { mount: mountLogin },
  overview: { mount: mountOverview },
  instances: { mount: mountInstances },
  jobs: { mount: mountJobs },
  artifacts: { mount: mountArtifacts },
  templates: { mount: mountTemplates },
  admin: { mount: mountAdmin, admin: true },
};
