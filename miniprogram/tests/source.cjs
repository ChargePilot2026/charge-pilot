const fs = require('node:fs');
const path = require('node:path');
const { stripTypeScriptTypes } = require('node:module');
const root = path.resolve(__dirname, '..');

function controllerPath(relative) {
  return path.join(root, 'src', relative.replace(/\.(js|ts)$/, '.controller.js'));
}
function controllerSource(filename) {
  return fs.readFileSync(filename, 'utf8')
    .replace(/^import \{ defineController, getApp, wx \} from .*;\s*/m, '')
    .replace(/^import (\{[^}]+\}) from ([^;]+);/gm, 'const $1 = require($2);')
    .replace('export default defineController(', 'Page(');
}
function sessionSource() {
  return stripTypeScriptTypes(fs.readFileSync(path.join(root, 'src/runtime/session.js'), 'utf8')
    .replace(/^import .*;\s*/m, '')
    .replace('export const application = {', 'App({')
    .replace(/};\s*export function getApp\(\) \{ return application; \}\s*$/, '});')
    .replace("process.env.TARO_APP_API_BASE || '/api/v1'", "'/api/v1'"));
}
module.exports = { controllerPath, controllerSource, sessionSource };
