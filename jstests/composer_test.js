// Table tests for composerState, the pure core of the composer's character counter and
// Post-button enable rule. Run with:  node --test jstests/*.js
//
// app.js is a plain browser script that wires DOM listeners at load time, so before
// requiring it we install a recursive proxy for `document`/`window`: every property
// access and call returns another callable proxy (ultimately null), which lets the
// top-level `document.body.addEventListener(...)` and the init IIFEs run as harmless
// no-ops. Only the exported pure functions are under test.
//
// The load-bearing invariant these tests pin: a plain post (or reply) with non-whitespace
// body text ALWAYS enables the Post button. A stale-cached app.js once made it look as
// though only selecting a template enabled it; the committed code never had that bug, and
// this table exists so a future refactor of the enable condition can't introduce it.

const { test } = require('node:test');
const assert = require('node:assert');

function makeStub() {
    const fn = function () { return null; };
    return new Proxy(fn, {
        get() { return makeStub(); },
        apply() { return null; },
    });
}
global.document = makeStub();
global.window = makeStub();

const { composerState } = require('../static/app.js');

// disabledFor is a thin readability wrapper: we only assert on the boolean enable state.
function disabledFor(text, suffix, hasQuoteContext) {
    return composerState(text, suffix, hasQuoteContext).disabled;
}

// (text, suffix, hasQuoteContext) -> expected `disabled`, with the human intent.
// `hasQuoteContext === false` covers BOTH plain-post and reply mode: reply never changes
// the rule, so it shares every false-quote row here.
const cases = [
    // The regression the report feared: plain body text, no template, no quote -> ENABLED.
    ['plain body text enables',                 'hello world',    '',       false, false],
    ['single char enables',                     'h',              '',       false, false],
    ['empty + nothing stays disabled',          '',               '',       false, true],
    ['whitespace-only stays disabled',          '   \n\t ',       '',       false, true],
    ['newline-only stays disabled',             '\n\n',           '',       false, true],

    // Bare quote-repost: empty body is a valid post when a quote is attached.
    ['bare quote (empty body) enables',         '',               '',       true,  false],
    ['whitespace-only + quote enables',         '   ',            '',       true,  false],
    ['quote + real body enables',               'nice',           '',       true,  false],

    // Template suffix: a suffix alone is not a postable body (no user text).
    ['suffix but empty body stays disabled',    '',               '#NHL',   false, true],
    ['body + suffix within limit enables',      'go devils',      '#NHL',   false, false],

    // Over-limit is disabled regardless of mode.
    ['body over 300 graphemes disabled',        'a'.repeat(301),  '',       false, true],
    ['exactly 300 graphemes enabled',           'a'.repeat(300),  '',       false, false],
    ['suffix pushes body over limit',           'a'.repeat(300),  '#x',     false, true],
    // Over-limit wins even with a quote attached (can't post an over-long body).
    ['over-limit + quote still disabled',       'a'.repeat(301),  '',       true,  true],
];

for (const [name, text, suffix, hasQuote, expectedDisabled] of cases) {
    test(name, () => {
        assert.strictEqual(disabledFor(text, suffix, hasQuote), expectedDisabled);
    });
}

// remaining is grapheme-counted (Gotcha 7): an emoji is one unit, not its UTF-16 length.
test('remaining counts graphemes, not UTF-16 code units', () => {
    const { remaining } = composerState('👨‍👩‍👧‍👦', '', false); // one family grapheme
    assert.strictEqual(remaining, 299);
});

// The suffix separator rule (Gotcha 4): a trailing newline in the body places the suffix
// on its own line (no separating space); otherwise a single space joins them.
test('combined joins body and suffix with a space by default', () => {
    assert.strictEqual(composerState('hi', '#x', false).combined, 'hi #x');
});
test('combined respects a trailing newline before the suffix', () => {
    assert.strictEqual(composerState('hi\n', '#x', false).combined, 'hi\n#x');
});
test('combined trims trailing spaces before the space separator', () => {
    // trailing spaces are stripped, then a single space separator is added
    assert.strictEqual(composerState('hi   ', '#x', false).combined, 'hi #x');
});
