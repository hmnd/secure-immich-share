const EFFECT_ALTERNATIVES =
	'$effect is not used in this codebase. Use derived state ($derived), event handlers, attachments ({@attach}), ' +
	'classes that start while read via createSubscriber, or explicit state machines. ' +
	'If truly unavoidable, disable local/no-effect inline with "-- <reason>".';

const EFFECT_CALLS = new Set(['pre', 'root']);

/** @typedef {Parameters<NonNullable<import('eslint').Rule.RuleListener['CallExpression']>>[0]} CallExpression */

/** @param {CallExpression['callee']} callee */
function isEffectCall(callee) {
	if (callee.type === 'Identifier') return callee.name === '$effect';
	return (
		callee.type === 'MemberExpression' &&
		!callee.computed &&
		callee.object.type === 'Identifier' &&
		callee.object.name === '$effect' &&
		callee.property.type === 'Identifier' &&
		EFFECT_CALLS.has(callee.property.name)
	);
}

/** @type {import('eslint').Rule.RuleModule} */
const noEffect = {
	meta: {
		type: 'problem',
		docs: { description: 'Disallow $effect, $effect.pre and $effect.root' },
		messages: { noEffect: EFFECT_ALTERNATIVES },
		schema: []
	},
	create(context) {
		return {
			CallExpression(node) {
				if (isEffectCall(node.callee)) context.report({ node, messageId: 'noEffect' });
			}
		};
	}
};

const DIRECTIVE = /^\s*(?:oxlint|eslint)-disable(?:-next-line|-line)?\b(.*)$/s;
const MIN_REASON_WORDS = 3;

/** @type {import('eslint').Rule.RuleModule} */
const effectDisableNeedsReason = {
	meta: {
		type: 'problem',
		docs: { description: 'Require a written reason when disabling local/no-effect' },
		messages: {
			needsReason:
				'Disabling local/no-effect needs a justification: append " -- <reason>" (at least ' +
				MIN_REASON_WORDS +
				' words) explaining why $effect is unavoidable here.'
		},
		schema: []
	},
	create(context) {
		return {
			Program() {
				for (const comment of context.sourceCode.getAllComments()) {
					const match = DIRECTIVE.exec(comment.value);
					if (!match || !comment.loc) continue;
					const [rules, ...rest] = match[1].split(/\s--\s/);
					const disablesEffect = !rules.trim() || /(?:^|[\s,])local\/no-effect\b/.test(rules);
					if (!disablesEffect) continue;
					const reason = rest.join(' -- ').trim();
					if (reason.split(/\s+/).filter(Boolean).length < MIN_REASON_WORDS) {
						context.report({ loc: comment.loc, messageId: 'needsReason' });
					}
				}
			}
		};
	}
};

/** @type {import('eslint').ESLint.Plugin} */
export default {
	meta: { name: 'local' },
	rules: {
		'no-effect': noEffect,
		'effect-disable-needs-reason': effectDisableNeedsReason
	}
};
