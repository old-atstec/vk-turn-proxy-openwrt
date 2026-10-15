'use strict';
'require view';
'require rpc';
'require poll';
'require ui';

// Status → VK TURN: every proto vkturn interface, from its daemon's status
// file through the rpcd object "vkturn".

const callStatus = rpc.declare({
	object: 'vkturn',
	method: 'status',
	expect: { interfaces: {} }
});

const callReconnect = rpc.declare({
	object: 'vkturn',
	method: 'reconnect',
	params: [ 'interface' ]
});

const stateLabel = {
	starting: _('Starting'),
	connecting: _('Connecting to the VK relays'),
	handshake: _('Waiting for the WireGuard handshake'),
	up: _('Up'),
	down: _('Down — no live session, retrying'),
	stopping: _('Stopping')
};

function ago(unix) {
	if (!unix)
		return _('never');
	const s = Math.max(0, Math.floor(Date.now() / 1000) - unix);
	return '%t'.format(s) + ' ' + _('ago');
}

function row(label, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'style': 'width:33%' }, [ label ]),
		E('td', { 'class': 'td left' }, [ value ])
	]);
}

function renderInterface(name, st) {
	const stale = !st.running || st.age > 30;
	let state = stateLabel[st.state] || st.state;
	if (stale)
		state = _('Not running');

	const rows = [
		row(_('State'), E('strong', {}, [ state ])),
		row(_('Connections'), '%d / %d'.format(st.conns_active, st.conns_configured)),
		row(_('Server'), '%s (%s)'.format(st.peer, (st.transport || '').toUpperCase())),
		row(_('WireGuard handshake'), ago(st.wg_last_handshake)),
		row(_('Received / sent (WireGuard)'), '%1024mB / %1024mB'.format(st.wg_rx_bytes, st.wg_tx_bytes)),
		row(_('Received / sent (TURN)'), '%1024mB / %1024mB'.format(st.rx_bytes, st.tx_bytes)),
		row(_('Credential pool'), _('%d of %d identities hold credentials, %d fresh, %d relays').format(st.pool_with_creds, st.pool_size, st.pool_fresh, st.pool_relays)),
		row(_('TURN round trip'), st.turn_rtt_ms ? '%.0f ms'.format(st.turn_rtt_ms) : '—'),
		row(_('Sessions since start'), '%d (%d watchdog restarts)'.format(st.sessions_total, st.watchdog_restarts)),
		row(_('WAN'), st.wan || '—'),
		row(_('Version'), st.commit ? '%s (%s)'.format(st.version, st.commit) : st.version)
	];

	if (Array.isArray(st.links) && st.links.length > 1)
		st.links.forEach(function(l, i) {
			const dead = l.dead_for_sec > 0
				? E('span', { 'class': 'label warning' }, [ _('call gone — retried in %d s').format(l.dead_for_sec) ])
				: _('live');
			rows.push(row(_('Call link %d').format(i + 1), E('span', {}, [
				dead, ' — ',
				_('%d connections on %d credentials; %d mints, %d failed').format(l.active, l.creds, l.mints, l.failures),
				l.last_error ? E('br') : '',
				l.last_error ? E('small', {}, [ l.last_error ]) : ''
			])));
		});
	if (st.quota_refusals > 0)
		rows.push(row(_('Relay refusals (486)'), '%d'.format(st.quota_refusals) + (st.mint_paused_sec > 0 ? ' — ' + _('minting paused for %d s').format(st.mint_paused_sec) : '')));
	if (st.auth_error)
		rows.push(row(_('VK login'), E('span', { 'class': 'label warning' }, [ st.auth_error ])));
	if (st.last_error)
		rows.push(row(_('Last error'), st.last_error));

	const notes = [];
	if (st.captcha_needed) {
		notes.push(E('div', { 'class': 'alert-message warning' }, [
			E('strong', {}, [ _('VK asks for a captcha.') ]), ' ',
			_('The automatic solver could not pass it; the daemon asks VK again every few minutes.'), ' ',
			st.captcha_url ? E('a', { 'href': st.captcha_url, 'target': '_blank', 'rel': 'noreferrer noopener' }, [ _('Open the captcha') ]) : ''
		]));
	}

	return E('div', { 'class': 'cbi-section' }, [
		E('h3', {}, [ name ]),
		...notes,
		E('table', { 'class': 'table' }, rows),
		E('div', { 'class': 'right' }, [
			E('button', {
				'class': 'btn cbi-button cbi-button-action',
				'disabled': stale ? '' : null,
				'click': ui.createHandlerFn({}, function() {
					return callReconnect(name).then(function(res) {
						if (res && res.error)
							ui.addNotification(null, E('p', {}, [ res.error ]));
					});
				})
			}, [ _('Reconnect') ])
		])
	]);
}

return view.extend({
	load() {
		return callStatus();
	},

	renderAll(ifaces) {
		const names = Object.keys(ifaces || {}).sort();
		if (names.length == 0)
			return E('p', { 'class': 'cbi-section' }, [ _('No VK TURN interface is running. Add one under Network → Interfaces (protocol "VK TURN (WireGuard)").') ]);
		return E('div', {}, names.map(n => renderInterface(n, ifaces[n])));
	},

	render(ifaces) {
		const box = E('div', { 'id': 'vkturn-status' }, [ this.renderAll(ifaces) ]);

		poll.add(L.bind(function() {
			return callStatus().then(L.bind(function(data) {
				box.replaceChildren(this.renderAll(data));
			}, this));
		}, this), 5);

		return E([], [
			E('h2', {}, [ _('VK TURN') ]),
			E('div', { 'class': 'cbi-map-descr' }, [ _('WireGuard over the TURN relays of VK calls.') ]),
			box
		]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
