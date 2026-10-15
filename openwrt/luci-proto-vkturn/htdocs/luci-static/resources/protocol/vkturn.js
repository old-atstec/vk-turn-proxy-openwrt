'use strict';
'require ui';
'require rpc';
'require form';
'require network';

// proto vkturn: WireGuard over VK's TURN relays (vk-turn-proxyd). The options
// are the protocol handler's (/lib/netifd/proto/vkturn.sh).

const callGenerateKeyPair = rpc.declare({
	object: 'vkturn',
	method: 'generateKeyPair',
	expect: { keys: {} }
});

const callGetPublicKey = rpc.declare({
	object: 'vkturn',
	method: 'getPublicKey',
	params: [ 'privkey' ],
	expect: { pubkey: '' }
});

function validateKey(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	if (value.length != 44 || !value.match(/^[A-Za-z0-9+\/]{43}=$/))
		return _('Invalid Base64 key string');
	return true;
}

function validateHostPort(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	const m = value.match(/^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+):(\d{1,5})$/);
	if (!m || +m[2] < 1 || +m[2] > 65535)
		return _('Expecting host:port');
	return true;
}

function validateVKLink(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	if (!value.match(/^https:\/\/(vk\.com|vk\.ru|m\.vk\.com|m\.vk\.ru)\/call\/join\/[A-Za-z0-9_-]+\/?$/))
		return _('Expecting a VK call link: https://vk.com/call/join/…');
	return true;
}

// The client's public key: what goes into the server's [Peer].
const cbiPublicKey = form.DummyValue.extend({
	cfgvalue(section_id) {
		return null;
	},

	renderWidget(section_id) {
		const node = E('code', { 'id': 'vkturn-pubkey-' + section_id }, [ '—' ]);
		const priv = this.section.formvalue(section_id, 'private_key') || this.map.data.get(this.map.config, section_id, 'private_key');

		if (priv && validateKey(section_id, priv) === true)
			callGetPublicKey(priv).then(pub => { node.textContent = pub || '—'; });

		return node;
	}
});

const cbiKeyPairGenerate = form.DummyValue.extend({
	cfgvalue(section_id, value) {
		return E('button', {
			'class': 'btn',
			'click': ui.createHandlerFn(this, function(section_id, ev) {
				const prv = this.section.getUIElement(section_id, 'private_key');
				const node = document.getElementById('vkturn-pubkey-' + section_id);

				return callGenerateKeyPair().then(function(keypair) {
					prv.setValue(keypair.priv);
					if (node)
						node.textContent = keypair.pub;
				});
			}, section_id)
		}, [ _('Generate new key pair') ]);
	}
});

// A connection issued by the server's panel (vkturn-panel):
// vkturn://import?c=<base64url(JSON)>, or the .vkturn file's JSON itself.
function parseProfile(text) {
	text = (text || '').trim();
	let json = text;
	const m = text.match(/vkturn:\/\/import\?c=([A-Za-z0-9_-]+)/);

	if (m) {
		let b64 = m[1].replace(/-/g, '+').replace(/_/g, '/');
		while (b64.length % 4)
			b64 += '=';
		const bin = atob(b64);
		const bytes = new Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++)
			bytes[i] = bin.charCodeAt(i);
		json = new TextDecoder('utf-8').decode(bytes);
	}
	else if (text.charAt(0) != '{') {
		throw new Error(_('Not a vkturn:// link or a .vkturn file'));
	}

	const p = JSON.parse(json);
	if (p.v != 1)
		throw new Error(_('Unsupported profile version: %s').format(p.v));
	for (const k of [ 'peer', 'server_key', 'private_key', 'address' ])
		if (typeof(p[k]) != 'string' || p[k] == '')
			throw new Error(_('The profile lacks %s').format(k));
	if (!Array.isArray(p.vk_links) || p.vk_links.length == 0)
		throw new Error(_('The profile has no VK call links'));
	return p;
}

// The import box: a link pasted here fills the form; Save applies it.
const cbiImport = form.DummyValue.extend({
	cfgvalue(section_id) {
		return null;
	},

	renderWidget(section_id) {
		const area = E('textarea', {
			'class': 'cbi-input-textarea',
			'rows': 3,
			'style': 'width:100%;font-family:monospace',
			'placeholder': 'vkturn://import?c=…'
		});
		const note = E('div', { 'class': 'cbi-value-description' });
		const section = this.section;

		const set = (name, value) => {
			const el = section.getUIElement(section_id, name);
			if (el && value != null && value !== '')
				el.setValue(value);
		};

		const apply = () => {
			let p;
			try {
				p = parseProfile(area.value);
			}
			catch (e) {
				note.textContent = e.message;
				note.style.color = 'red';
				return;
			}

			set('peer', p.peer);
			set('public_key', p.server_key);
			set('private_key', p.private_key);
			set('preshared_key', p.psk);
			set('addresses', [ p.address ]);
			set('vk_link', p.vk_links);
			if (p.conns)
				set('conns', String(p.conns));
			if (p.transport == 'udp' || p.transport == 'tcp')
				set('turn_transport', p.transport);
			if (p.mtu)
				set('mtu', String(p.mtu));

			const node = document.getElementById('vkturn-pubkey-' + section_id);
			if (node)
				callGetPublicKey(p.private_key).then(pub => { node.textContent = pub || '—'; });

			area.value = '';
			note.style.color = '';
			note.textContent = (p.name ? _('Imported "%s".').format(p.name) + ' ' : '') +
				_('The fields below are filled in — press Save & Apply.');
		};

		return E('div', {}, [
			area,
			E('div', { 'style': 'margin-top:4px' }, [
				E('button', { 'class': 'btn cbi-button-action', 'click': ui.createHandlerFn(this, apply) }, [ _('Import') ])
			]),
			note
		]);
	}
});

return network.registerProtocol('vkturn', {
	getI18n() {
		return _('VK TURN (WireGuard)');
	},

	getIfname() {
		return this._ubus('l3_device') || this.sid;
	},

	getPackageName() {
		return 'vk-turn-proxy';
	},

	isFloating() {
		return true;
	},

	isVirtual() {
		return true;
	},

	getDevices() {
		return null;
	},

	containsDevice(ifname) {
		return (network.getIfnameOf(ifname) == this.getIfname());
	},

	renderFormOptions(s) {
		let o;

		s.taboption('general', cbiImport, '_import', _('Import'),
			_('A connection from the server panel: paste its <code>vkturn://</code> link (or the <code>.vkturn</code> file) and press Import.'));

		o = s.taboption('general', form.Value, 'peer', _('Server'),
			_('The server\'s SRTP listener (vk-turn-proxy server, <code>-srtp</code>): <code>host:port</code>.'));
		o.rmempty = false;
		o.validate = validateHostPort;
		o.placeholder = '203.0.113.10:56004';

		o = s.taboption('general', form.Value, 'public_key', _('Server public key'),
			_('The WireGuard public key of the server.'));
		o.rmempty = false;
		o.validate = validateKey;

		o = s.taboption('general', form.Value, 'private_key', _('Private key'),
			_('The WireGuard private key of this router.'));
		o.password = true;
		o.rmempty = false;
		o.validate = validateKey;

		s.taboption('general', cbiPublicKey, '_public_key', _('Public key'),
			_('Add it as a peer on the server: <code>wg set &lt;iface&gt; peer &lt;key&gt; allowed-ips &lt;address&gt;/32</code>.'));
		s.taboption('general', cbiKeyPairGenerate, '_gen_keypair', ' ');

		o = s.taboption('general', form.Value, 'preshared_key', _('Preshared key'),
			_('Optional. A WireGuard preshared key shared with the server.'));
		o.password = true;
		o.validate = validateKey;

		o = s.taboption('general', form.DynamicList, 'addresses', _('IP addresses'),
			_('The tunnel address of this router, with the prefix (e.g. <code>10.66.66.2/24</code>).'));
		o.datatype = 'ipaddr';
		o.rmempty = false;

		o = s.taboption('general', form.DynamicList, 'vk_link', _('VK call links'),
			_('VK call links the TURN credentials are minted from. With several, the credentials alternate between them and a call VK closes is covered by the others: a reserve.'));
		o.validate = validateVKLink;
		o.rmempty = false;

		o = s.taboption('general', form.Value, 'conns', _('Connections'),
			_('TURN allocations, 1…120. Each carries ~2 Mbit/s (the server paces it under VK\'s per-allocation limit): 60 is ~95 Mbit/s.'));
		o.datatype = 'range(1,120)';
		o.placeholder = '20';

		o = s.taboption('general', form.ListValue, 'turn_transport', _('Transport to the VK relay'));
		o.value('udp', 'UDP');
		o.value('tcp', 'TCP');
		o.default = 'udp';

		o = s.taboption('advanced', form.Value, 'mtu', _('MTU'),
			_('1000…1400, 1280 by default.'));
		o.datatype = 'range(1000,1400)';
		o.placeholder = '1280';

		o = s.taboption('advanced', form.Value, 'keepalive', _('Keepalive'),
			_('WireGuard persistent keepalive, seconds.'));
		o.datatype = 'range(0,3600)';
		o.placeholder = '25';

		o = s.taboption('advanced', form.Value, 'down_after', _('Down after'),
			_('Seconds without a live session before the interface goes down (and comes back up by itself).'));
		o.datatype = 'range(5,86400)';
		o.placeholder = '60';

		o = s.taboption('advanced', form.Value, 'turn_server', _('TURN server'),
			_('Optional. A TURN relay of your choice instead of VK\'s: <code>host:port</code>.'));
		o.validate = validateHostPort;

		o = s.taboption('advanced', form.Value, 'pool_reserve', _('Credential reserve'),
			_('Reserve credential sets. One set rides a network change without minting.'));
		o.datatype = 'range(0,10)';
		o.placeholder = '1';

		o = s.taboption('advanced', form.Value, 'uplink_pace', _('Uplink pacer'),
			_('KiB/s per connection, 0 = off.'));
		o.datatype = 'uinteger';
		o.placeholder = '0';

		o = s.taboption('advanced', form.Flag, 'spread_relays', _('All relays of the call'),
			_('Spread the connections over every TURN relay VK gives for the call, not only the first. Anonymous mode.'));
		o.default = o.enabled;

		o = s.taboption('advanced', form.Value, 'cpu_affinity', _('CPU affinity'),
			_('Optional. A CPU mask the daemon runs on, as <code>taskset</code> takes it. On big.LITTLE SoCs the big cores cost far less per packet: <code>0x30</code> is the two Cortex-A72 of an RK3399 (NanoPi R4S).'));
		o.validate = function(section_id, value) {
			if (value == null || value.length == 0 || /^(0x[0-9a-fA-F]+|[0-9]+)$/.test(value))
				return true;
			return _('A CPU mask such as 0x30');
		};

		o = s.taboption('advanced', form.Value, 'cred_cache', _('Credential cache'),
			_('Where the TURN credentials are kept between restarts. On flash it survives a reboot; under <code>/var/run</code> it does not. Default: <code>/etc/vkturn/&lt;interface&gt;/creds.json</code>.'));
		o.placeholder = '/etc/vkturn/<interface>/creds.json';

		o = s.taboption('advanced', form.Value, 'vk_cookie_file', _('VK login cookies'),
			_('Optional. A browser\'s cookies.txt of a VK login: the logged-in mode.'));

		o = s.taboption('advanced', form.Value, 'fwmark', _('Firewall mark'),
			_('Optional. A mark on the proxy\'s own sockets, so that no policy routing catches them. <code>0x200000</code> is the one NetShift lets pass.'));
		o.validate = function(section_id, value) {
			if (value == null || value.length == 0)
				return true;
			if (!value.match(/^(0x[0-9a-fA-F]{1,8}|[0-9]{1,10})$/) || +value > 4294967295)
				return _('Expecting a hexadecimal (0x…) or decimal mark');
			return true;
		};

		o = s.taboption('advanced', form.Value, 'bind_device', _('Bind to'),
			_('Optional. Bind the proxy\'s own sockets to this device or interface (e.g. <code>wan</code>).'));

		o = s.taboption('advanced', form.Value, 'dns_server', _('DNS server'),
			_('Optional. Resolve VK\'s hosts with this server instead of the router\'s resolver.'));
		o.datatype = 'or(ipaddr,ipaddrport(1))';

		o = s.taboption('advanced', form.ListValue, 'log_level', _('Log level'));
		o.value('info', _('Info'));
		o.value('debug', _('Debug'));
		o.default = 'info';

		o = s.taboption('advanced', form.Value, 'stats_every', _('Statistics interval'),
			_('Seconds between statistics lines in the log, 0 = none.'));
		o.datatype = 'range(0,86400)';
		o.placeholder = '0';
	}
});
