import fs from 'fs';

// Exact media ranges override wildcards, including explicit q=0 exclusions.
// Only UTF-8 is served; an unsupported media parameter does not match it.
function quality(accept, subtype) {
    let bestSpecificity = -1;
    let bestQuality = 0;

    const ranges = accept.toLowerCase().split(',');
    for (let i = 0; i < ranges.length; i++) {
        const range = ranges[i];
        const parts = range.split(';').map(part => part.trim());
        const media = parts.shift();
        let specificity;
        if (media === 'text/' + subtype) specificity = 2;
        else if (media === 'text/*') specificity = 1;
        else if (media === '*/*') specificity = 0;
        else continue;

        let q = 1;
        let hasQuality = false;
        let hasCharset = false;
        let matches = true;
        for (let j = 0; j < parts.length; j++) {
            const parameter = parts[j];
            const pair = parameter.split('=');
            const name = pair.shift().trim();
            const value = pair.join('=').trim();
            if (name === 'q') {
                if (hasQuality || !/^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$/.test(value)) {
                    matches = false;
                    break;
                }
                hasQuality = true;
                q = Number(value);
            } else if (!hasQuality) {
                // Parameters before q constrain the media type; later ones are extensions.
                if (hasCharset || name !== 'charset' || !/^(utf-8|"utf-8")$/.test(value)) {
                    matches = false;
                    break;
                }
                hasCharset = true;
            }
        }
        // A charset breaks ties within a media range, without outweighing its type.
        specificity = specificity * 2 + Number(hasCharset);
        if (matches && specificity >= bestSpecificity) {
            bestQuality = specificity > bestSpecificity ? q : Math.max(bestQuality, q);
            bestSpecificity = specificity;
        }
    }
    return bestQuality;
}

function hasPageRepresentations(r) {
    const directory = r.variables.document_root + r.variables.page_directory;
    return fs.existsSync(directory + 'index.html') && fs.existsSync(directory + 'index.md');
}

function index(r) {
    // Missing pages retain nginx's ordinary 404, regardless of Accept.
    if (!hasPageRepresentations(r)) return 'index.html';
    const accept = r.headersIn.Accept;
    if (!accept || !accept.trim()) return 'index.html';
    const htmlQuality = quality(accept, 'html');
    const markdownQuality = quality(accept, 'markdown');
    if (htmlQuality === 0 && markdownQuality === 0) return 'unacceptable';
    return markdownQuality > htmlQuality ? 'index.md' : 'index.html';
}

function links(r) {
    let discovery = '</llms.txt>; rel="describedby"; type="text/plain", '
        + '</auth.md>; rel="describedby"; type="text/markdown", '
        + '</index.xml>; rel="alternate"; type="application/rss+xml", '
        + '</sitemap.xml>; rel="sitemap"; type="application/xml"';
    if (r.variables.page_directory === '/' || r.uri === '/api/content.json') {
        discovery += ', </.well-known/api-catalog>; rel="api-catalog"; type="application/linkset+json", '
            + '</api/openapi.json>; rel="service-desc"; type="application/json", '
            + '</api/>; rel="service-doc"; type="text/html", '
            + '</.well-known/agent-skills/index.json>; rel="describedby"; type="application/json"';
    }
    if (!hasPageRepresentations(r)) return discovery;
    const directory = r.variables.page_directory;
    const alternate = r.uri.endsWith('.md')
        ? '<' + directory + '>; rel="alternate"; type="text/html"'
        : '<' + directory + 'index.md>; rel="alternate"; type="text/markdown"';
    return '<' + directory + '>; rel="canonical", ' + alternate + ', ' + discovery;
}

export default { index, links };
