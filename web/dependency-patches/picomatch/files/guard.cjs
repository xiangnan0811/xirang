'use strict';

// Only parser-created states are associated with checked output. Raw sources and
// caller-modified states retain picomatch's existing raw-regex behavior.
const checked = new WeakMap();
const nativeErrors = new WeakSet();
const candidate = /\)[*+{]/;

const grammar = options => {
  const flags = options.flags || (options.nocase ? 'i' : '');
  return { flags, unicode: flags.includes('u'), unicodeSets: flags.includes('v') };
};

const inspect = (source, mode) => {
  const { RegExpParser, RegExpSyntaxError, visitRegExpAST } = require('@eslint-community/regexpp');
  let depth = 0;
  const assertions = [];
  let risky = false;
  const atom = () => { if (depth >= 2) risky = true; };
  try {
    const ast = new RegExpParser().parsePattern(source, 0, source.length, mode);
    visitRegExpAST(ast, {
      onQuantifierEnter(node) { if (node.max > 1) depth++; },
      onQuantifierLeave(node) { if (node.max > 1) depth--; },
      onAssertionEnter(node) {
        if (node.kind === 'lookahead' || node.kind === 'lookbehind') {
          assertions.push(depth);
          depth = 0;
        }
      },
      onAssertionLeave(node) {
        if (node.kind === 'lookahead' || node.kind === 'lookbehind') depth = assertions.pop();
      },
      onCharacterSetEnter: atom,
      onCharacterClassEnter: atom,
      onExpressionCharacterClassEnter: atom,
      onBackreferenceEnter: atom
    });
  } catch (error) {
    if (error instanceof RegExpSyntaxError || error instanceof RangeError) {
      throw new SyntaxError('Cannot classify generated picomatch expression', { cause: error });
    }
    throw error;
  }
  return risky;
};

const literal = input => {
  let output = '';
  for (let i = 0; i < input.length; i++) {
    output += `\\u${input.charCodeAt(i).toString(16).padStart(4, '0')}`;
  }
  return output;
};
exports.literal = literal;

const classify = (source, body, prefix, mode, wrap, details) => {
  if (!candidate.test(source)) return source;
  try {
    // Preserve the existing toRegex/debug behavior for invalid compiler flags.
    new RegExp('', mode.flags);
  } catch (error) {
    if (error instanceof SyntaxError) return '(?!)';
    throw error;
  }
  let risky;
  try {
    risky = inspect(source, mode);
  } catch (error) {
    const { RegExpSyntaxError } = require('@eslint-community/regexpp');
    if (!(error instanceof SyntaxError && error.cause instanceof RegExpSyntaxError)) throw error;
    try {
      new RegExp(`(?:${source})`, mode.flags);
    } catch (nativeError) {
      if (!(nativeError instanceof SyntaxError)) throw nativeError;
      nativeErrors.add(nativeError);
      details.invalid = nativeError;
      return '(?!)';
    }
    throw error;
  }
  if (!risky) return source;
  const fallback = wrap(prefix + literal(body));
  // Always parse the fallback, even when the quick candidate filter misses it.
  if (inspect(fallback, mode)) {
    throw new SyntaxError('Unsafe picomatch fallback', { cause: new SyntaxError('Risky prepend') });
  }
  return fallback;
};

exports.finalize = (state, body, prefix, options, wrap = value => value) => {
  const original = state.output;
  if (!candidate.test(original)) return original;
  const mode = grammar(options);
  const details = {};
  state.output = classify(original, body, prefix, mode, wrap, details);
  if (state.output !== original && Array.isArray(state.tokens)) {
    state.tokens = [{ type: 'bos', value: '', output: state.output }];
  }
  checked.set(state, { original, output: state.output, body, prefix, mode, wrap, invalid: details.invalid });
  return state.output;
};

exports.output = (state, options, returnOutput = false) => {
  const record = checked.get(state);
  if (!record || state.output !== record.output) return state.output;
  const mode = grammar(options);
  let output = record.output;
  let invalid = record.invalid;
  if (mode.unicode !== record.mode.unicode || mode.unicodeSets !== record.mode.unicodeSets || mode.flags !== record.mode.flags) {
    const details = {};
    output = classify(record.original, record.body, record.prefix, mode, record.wrap, details);
    invalid = details.invalid;
  }
  if (invalid && !returnOutput) return invalid;
  return output;
};
exports.isNativeError = (state, value) => {
  const record = checked.get(state);
  return !!record && state.output === record.output && nativeErrors.has(value);
};
