// rpcd plugin: the ubus object "vkturn" — the status of every proto vkturn
// interface and its controls.
//
//   ubus call vkturn status                    every interface, by name
//   ubus call vkturn status '{"interface":"vkturn"}'
//   ubus call vkturn reconnect '{"interface":"vkturn"}'
//   ubus call vkturn generateKeyPair
//   ubus call vkturn getPublicKey '{"privkey":"…"}'
//
// The status is the daemon's own file, /var/run/vkturn/<iface>/status.json
// (cmd/vk-turn-proxyd/status.go). No private key ever goes on a command
// line: getPublicKey hands it to the daemon's pubkey on stdin.

'use strict';

import { readfile, popen, glob, unlink, stat, basename, mkdir } from 'fs';

const RUN = '/var/run/vkturn';
const BIN = '/usr/sbin/vk-turn-proxyd';

function validName(name) {
	return type(name) == 'string' && match(name, /^[A-Za-z0-9_.-]{1,15}$/) != null;
}

function readStatus(name) {
	const raw = readfile(`${RUN}/${name}/status.json`);
	if (!raw)
		return null;
	const st = json(raw);
	if (type(st) != 'object')
		return null;
	st.age = time() - (st.updated ?? 0);
	st.running = daemonPid(name, st.pid) != null;
	return st;
}

// daemonPid: pid, if it is the daemon running this interface's config.
function daemonPid(name, pid) {
	if (type(pid) != 'int' || pid <= 1)
		return null;
	const cmd = readfile(`/proc/${pid}/cmdline`);
	if (!cmd)
		return null;
	const argv = split(cmd, '\0');
	if (argv[0] != BIN || index(argv, `${RUN}/${name}/config`) < 0)
		return null;
	return pid;
}

function command(cmd) {
	const p = popen(cmd);
	if (!p)
		return null;
	const out = trim(p.read('all') ?? '');
	p.close();
	return out;
}

function publicKeyOf(priv) {
	if (type(priv) != 'string' || match(priv, /^[A-Za-z0-9+\/]{43}=$/) == null)
		return null;
	// rpcd calls a plugin one request at a time: the name cannot collide.
	const tmp = `${RUN}/.pubkey.${time()}`;
	mkdir(RUN, 448); // 0700; an existing one is kept as it is
	const p = popen(`${BIN} pubkey > ${tmp} 2>/dev/null`, 'w');
	if (!p)
		return null;
	p.write(priv + '\n');
	p.close();
	const pub = trim(readfile(tmp) ?? '');
	unlink(tmp);
	return length(pub) ? pub : null;
}

const methods = {
	status: {
		args: { interface: 'interface' },
		call: function(req) {
			const name = req.args?.interface;
			if (name != null) {
				if (!validName(name))
					return { error: 'invalid interface name' };
				return readStatus(name) ?? { error: 'no status: the interface is not running' };
			}
			const out = {};
			for (let dir in glob(`${RUN}/*`)) {
				const n = basename(dir);
				if (validName(n) && stat(dir)?.type == 'directory') {
					const st = readStatus(n);
					if (st)
						out[n] = st;
				}
			}
			return { interfaces: out };
		}
	},

	reconnect: {
		args: { interface: 'interface' },
		call: function(req) {
			const name = req.args?.interface;
			if (!validName(name))
				return { error: 'invalid interface name' };
			const st = readStatus(name);
			const pid = daemonPid(name, st?.pid);
			if (pid == null)
				return { error: 'the daemon is not running' };
			return { result: system(['kill', '-USR1', `${pid}`]) == 0 };
		}
	},

	generateKeyPair: {
		call: function() {
			const priv = command(`${BIN} genkey 2>/dev/null`);
			const pub = publicKeyOf(priv);
			if (!priv || !pub)
				return { error: 'key generation failed' };
			return { keys: { priv, pub } };
		}
	},

	getPublicKey: {
		args: { privkey: 'privkey' },
		call: function(req) {
			const pub = publicKeyOf(req.args?.privkey);
			return pub ? { pubkey: pub } : { error: 'not a WireGuard private key' };
		}
	}
};

return { vkturn: methods };
