// modification.js — lampa-go runtime integration.
// Rewrites requests to the cub mirrors into our /cub/ proxy namespace.
// Lampa loads this file automatically from the hosting server
// (see src/core/plugins.js in lampa-source), so it needs no patching.
(function () {
	'use strict';

	var MIRRORS = ['cub.best', 'cub.black', 'durex.monster', 'cubnotrip.top'];
	var MARKERS = ['tmdb', 'geo', 'ws', 'imagetmdb', 'cdn', 'ad'];

	function ownHost(host) {
		return (
			host === location.hostname ||
			host.indexOf('.' + location.hostname, host.length - location.hostname.length - 1) !== -1
		);
	}

	Lampa.Listener.follow('request_before', function (e) {
		var url = e.params.url;
		if (typeof url !== 'string' || url.indexOf('/cub/') !== -1) return;

		var match = url.match(/^https?:\/\/([^\/?#]+)([^?#]*)(\?[^#]*)?/);
		if (!match) return;

		var host = match[1].toLowerCase();
		var path = match[2] || '/';
		var query = match[3] || '';

		if (!ownHost(host) && MIRRORS.indexOf(host) === -1) return;

		var marker = '';
		var labels = host.split('.');
		if (labels.length > 1 && MARKERS.indexOf(labels[0]) !== -1) {
			marker = labels[0] + '/';
		}

		if (path === '/') path = '';

		e.params.url =
			location.origin + '/cub/' + marker + path.replace(/^\//, '') + query;
	});
})();
