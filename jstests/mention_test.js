// Table tests for findMentionQuery, the composer's pure @-mention token detector.
// Run with:  node --test jstests/*.js
//
// app.js is a plain browser script that wires DOM listeners at load time, so before
// requiring it we install a recursive proxy for `document`/`window`: every property
// access and call returns another callable proxy (ultimately null), which lets the
// top-level `document.body.addEventListener(...)` and the init IIFEs run as harmless
// no-ops. Only the exported findMentionQuery is under test.

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

const { findMentionQuery } = require('../static/app.js');

test('caret mid-token returns the partial query', () => {
    const text = 'hello @rus world';
    // caret right after "@rus" (index 10)
    const got = findMentionQuery(text, 10);
    assert.deepStrictEqual(got, { query: 'rus', start: 6 });
});

test('caret at end of a trailing token', () => {
    const text = 'hey @roger';
    const got = findMentionQuery(text, text.length);
    assert.deepStrictEqual(got, { query: 'roger', start: 4 });
});

test('@ at the very start of the text is a token', () => {
    const text = '@rus';
    const got = findMentionQuery(text, 4);
    assert.deepStrictEqual(got, { query: 'rus', start: 0 });
});

test('caret after whitespace is not in a token', () => {
    const text = '@foo bar';
    // caret after the space (index 5)
    assert.strictEqual(findMentionQuery(text, 5), null);
});

test('caret immediately after the token-ending space is not in a token', () => {
    const text = '@foo ';
    assert.strictEqual(findMentionQuery(text, 5), null);
});

test('@ mid-word (email) is not a token', () => {
    const text = 'mail me at foo@bar';
    // caret at end, inside "bar"; the char before '@' is 'o' (not whitespace)
    assert.strictEqual(findMentionQuery(text, text.length), null);
});

test('email token is rejected even with the caret right after the @', () => {
    const text = 'foo@';
    assert.strictEqual(findMentionQuery(text, 4), null);
});

test('multiple @s: caret in the second token reads the second query', () => {
    const text = 'hi @foo @ba';
    // caret at end, inside the second token "@ba"
    const got = findMentionQuery(text, text.length);
    assert.deepStrictEqual(got, { query: 'ba', start: 8 });
});

test('multiple @s: caret still inside the first token reads the first query', () => {
    const text = '@foo @bar';
    // caret after "@foo" (index 4), before the space
    const got = findMentionQuery(text, 4);
    assert.deepStrictEqual(got, { query: 'foo', start: 0 });
});

test('emoji before the token: JS string indices, query agrees on TEXT', () => {
    // "🎉" is a surrogate pair (JS length 2). The server later computes UTF-8 BYTE
    // offsets independently; these JS indices need only agree on the inserted text, not
    // on the server's byte math. The detected query is still exactly "rus".
    const text = '🎉 @rus';
    assert.strictEqual(text.length, 7); // 2 (emoji) + 1 (space) + 4 ("@rus")
    const got = findMentionQuery(text, 7);
    assert.deepStrictEqual(got, { query: 'rus', start: 3 });
    // The slice used for insertion recovers the '@partial' token verbatim.
    assert.strictEqual(text.slice(got.start, 7), '@rus');
});

test('just "@" with the caret after it has an empty query (start still set)', () => {
    // The caller (handleMentionInput) treats a zero-length query as "not yet searching",
    // but the pure function still reports the token so the next typed char extends it.
    const got = findMentionQuery('@', 1);
    assert.deepStrictEqual(got, { query: '', start: 0 });
});

test('newline before the token counts as a boundary', () => {
    const text = 'line one\n@rus';
    const got = findMentionQuery(text, text.length);
    assert.deepStrictEqual(got, { query: 'rus', start: 9 });
});

test('out-of-range caret and non-string input return null', () => {
    assert.strictEqual(findMentionQuery('@rus', -1), null);
    assert.strictEqual(findMentionQuery('@rus', 99), null);
    assert.strictEqual(findMentionQuery(null, 0), null);
});
