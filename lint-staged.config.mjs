// lint-staged configuration.
//
// Vendored third-party bundles (e.g. services/**/internal/render/vendor/**) and
// generated code (packages/schema/generated/**) are NOT our source and must
// never be linted or formatted: they are pinned upstream artifacts and the
// pre-commit hook auto-stages regenerated proto bindings. Formatting them would
// corrupt a minified bundle and running eslint over a 3MB vendored library is
// both wrong and slow. We therefore filter those paths out before handing the
// remaining files to each formatter/linter.
//
// This function form (rather than the plain glob→command map) is what lets us
// exclude a subtree: a bare `*.js` glob is basename-matched across the whole
// tree, so it cannot otherwise skip a vendored directory.

const isOurSource = (f) => !f.includes('/vendor/') && !f.includes('/generated/');

export default {
  '*.{ts,tsx,js,jsx}': (files) => {
    const f = files.filter(isOurSource);
    if (f.length === 0) return [];
    const list = f.join(' ');
    return [`eslint --fix ${list}`, `prettier --write ${list}`];
  },
  '*.{json,md,yaml,yml}': (files) => {
    const f = files.filter(isOurSource);
    if (f.length === 0) return [];
    return [`prettier --write ${f.join(' ')}`];
  },
  '*.py': (files) => {
    const f = files.filter(isOurSource);
    if (f.length === 0) return [];
    const list = f.join(' ');
    return [`ruff check --fix ${list}`, `ruff format ${list}`];
  },
  '*.go': (files) => {
    const f = files.filter(isOurSource);
    if (f.length === 0) return [];
    return [`gofmt -w ${f.join(' ')}`];
  },
  '*.rs': (files) => {
    const f = files.filter(isOurSource);
    if (f.length === 0) return [];
    return [`rustfmt ${f.join(' ')}`];
  },
};
