async function fetchJSON(url, opts = {}) {
  const res = await fetch(url, opts)
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}
let map = null
let geojsonLayer = null
let routeLayer = null

function colorForStatus(status) {
  switch (status) {
    case 'rojo': return '#e74c3c'
    case 'amarillo': return '#f1c40f'
    default: return '#2ecc71'
  }
}

function styleFunc(feature) {
  const status = (feature.properties && feature.properties.status) || 'verde'
  return {
    color: '#333',
    weight: 1,
    fillColor: colorForStatus(status),
    fillOpacity: 0.6,
  }
}

function onEachFeature(feature, layer) {
  const name = feature.properties && feature.properties.name
  const status = feature.properties && feature.properties.status || 'verde'
  layer.bindPopup(`<strong>${name}</strong><br/>Estado: ${status}`)
}

async function initMap() {
  if (typeof L === 'undefined') {
    console.warn('Leaflet no cargado')
    return
  }
  const mapContainer = document.getElementById('leaflet-map')
  if (!mapContainer) {
    console.error('Map container not found')
    return
  }
  map = L.map('leaflet-map').setView([-11.98, -77.02], 12)
  L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '© OpenStreetMap contributors'
  }).addTo(map)
  
  // Wait a bit for map to render
  await new Promise(r => setTimeout(r, 100))
  await loadZones()
}

async function loadZones() {
  try {
    const data = await fetchJSON('/api/zones_geojson')
    if (!map) {
      console.warn('Map not initialized yet')
      return
    }
    if (!data || !data.features || data.features.length === 0) {
      console.warn('No zones data received')
      return
    }
    if (geojsonLayer) {
      geojsonLayer.clearLayers()
      geojsonLayer.addData(data)
      // redraw evacuation routes when zones update
      await drawEvacRoutes(data)
    } else {
      geojsonLayer = L.geoJSON(data, { style: styleFunc, onEachFeature: onEachFeature }).addTo(map)
      try {
        const bounds = geojsonLayer.getBounds()
        if (bounds.isValid()) {
          map.fitBounds(bounds, { padding: [20,20] })
        }
      } catch (e) {
        console.warn('Error fitting bounds:', e)
      }
      // initial evacuation routes
      await drawEvacRoutes(data)
    }
  } catch (e) {
    console.error('loadZones error:', e)
  }
}

function computeCentroid(feature) {
  // very simple centroid: average of first ring coordinates
  try {
    const coords = feature.geometry && feature.geometry.coordinates
    if (!coords) return null
    // For Polygon, coords[0] is the outer ring (array of [lon,lat])
    const ring = coords[0]
    let sx = 0, sy = 0, n = 0
    for (const p of ring) {
      sx += p[0]
      sy += p[1]
      n++
    }
    if (n === 0) return null
    const lon = sx / n
    const lat = sy / n
    return [lat, lon]
  } catch (e) {
    return null
  }
}

async function drawEvacRoutes(data) {
  if (!map || !data || !data.features) return
  if (routeLayer) {
    routeLayer.clearLayers()
  } else {
    routeLayer = L.layerGroup().addTo(map)
  }

  // Default safe point (lat, lon). Can be overridden per feature via properties.shelter = [lat,lon]
  const defaultSafe = [-11.96, -77.03]

  // Use sequential requests to avoid hammering public OSRM demo server
  for (const f of data.features) {
    try {
      const c = computeCentroid(f)
      if (!c) {
        console.warn('No centroid for feature', f.properties?.name)
        continue
      }

      const fromLat = c[0]
      const fromLon = c[1]

      let safe = defaultSafe
      if (f.properties && f.properties.shelter && Array.isArray(f.properties.shelter) && f.properties.shelter.length >= 2) {
        safe = f.properties.shelter
      }
      const toLat = safe[0]
      const toLon = safe[1]

      // Add marker for shelter point
      const shelterIcon = L.icon({
        iconUrl: 'data:image/svg+xml;base64,' + btoa('<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="#2ecc71"><path d="M12 2C8.13 2 5 5.13 5 9c0 5.25 7 13 7 13s7-7.75 7-13c0-3.87-3.13-7-7-7zm0 9.5c-1.38 0-2.5-1.12-2.5-2.5s1.12-2.5 2.5-2.5 2.5 1.12 2.5 2.5-1.12 2.5-2.5 2.5z"/></svg>'),
        iconSize: [24, 24],
        iconAnchor: [12, 24],
        popupAnchor: [0, -24]
      })
      const shelterMarker = L.marker([toLat, toLon], { icon: shelterIcon })
      shelterMarker.bindPopup(`<strong>Punto de Refugio</strong><br/>${(f.properties && f.properties.name) || 'Zona'}`)
      routeLayer.addLayer(shelterMarker)

      // Request server-side route (server will proxy to OSRM and cache)
      try {
        const url = `/api/route?from=${fromLon},${fromLat}&to=${toLon},${toLat}`
        const resp = await fetchJSON(url)
        if (resp && resp.routes && resp.routes.length > 0 && resp.routes[0].geometry) {
          const coords = resp.routes[0].geometry.coordinates // array of [lon,lat]
          const latlngs = coords.map(p => [p[1], p[0]])
          const routeLine = L.polyline(latlngs, { color: '#e74c3c', weight: 3, opacity: 0.85 })
          routeLine.bindPopup(`<strong>${(f.properties && f.properties.name) || 'Zona'}</strong><br/>Ruta de evacuación`)
          routeLayer.addLayer(routeLine)
        } else {
          // fallback: straight dashed line
          const from = L.latLng(fromLat, fromLon)
          const to = L.latLng(toLat, toLon)
          const line = L.polyline([from, to], { color: '#e74c3c', weight: 2, dashArray: '6,6' })
          line.bindPopup(`<strong>${(f.properties && f.properties.name) || 'Zona'}</strong><br/>Ruta de evacuación (directa)`)
          routeLayer.addLayer(line)
        }
      } catch (routeErr) {
        console.warn('Error fetching route for', f.properties?.name, routeErr)
        // fallback: straight dashed line even on error
        const from = L.latLng(fromLat, fromLon)
        const to = L.latLng(toLat, toLon)
        const line = L.polyline([from, to], { color: '#e74c3c', weight: 2, dashArray: '6,6' })
        line.bindPopup(`<strong>${(f.properties && f.properties.name) || 'Zona'}</strong><br/>Ruta de evacuación (directa)`)
        routeLayer.addLayer(line)
      }
      // small delay to be polite with the public OSRM demo server
      await new Promise(r => setTimeout(r, 120))
    } catch (e) {
      console.warn('route error for feature', f.properties?.name, e)
    }
  }
}

function badgeFor(alert) {
  const color = alert.severidad === 'crítica' ? 'rojo' : (alert.severidad === 'alta' ? 'amarillo' : 'verde')
  return `<span class="badge ${color}">${alert.severidad}</span>`
}

async function refreshAlerts() {
  try {
    const alerts = await fetchJSON('/api/alerts')
    const list = document.getElementById('alerts')
    list.innerHTML = alerts.map(a => {
      const t = new Date(a.timestamp)
      const meta = `${a.zona} • ${t.toLocaleTimeString()}${a.extracto ? ' • ' + a.extracto : ''}`
      return `<li><div><strong>${a.tipo}</strong><div class="meta">${meta}</div><div>${a.mensaje}</div></div>${badgeFor(a)}</li>`
    }).join('')
  } catch (e) {
    console.error('alerts', e)
  }
}

async function submitSMS(ev) {
  ev.preventDefault()
  const zona = document.getElementById('zona').value
  const texto = document.getElementById('texto').value
  if (!texto.trim()) return
  await fetchJSON('/api/sms', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ zona, texto }),
  })
  document.getElementById('texto').value = ''
  // dar tiempo a que el worker procese
  setTimeout(() => { refreshAlerts(); loadZones(); }, 150)
}

async function resetZones() {
  await fetch('/api/reset', { method: 'POST' })
  await loadZones()
}

document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('smsForm').addEventListener('submit', submitSMS)
  document.getElementById('resetBtn').addEventListener('click', resetZones)

  // Chat form (if present)
  const chatForm = document.getElementById('chatForm')
  const chatInput = document.getElementById('chatInput')
  if (chatForm && chatInput) {
    chatForm.addEventListener('submit', async (ev) => {
      ev.preventDefault()
      const text = chatInput.value || ''
      if (!text.trim()) return
      chatInput.value = ''
      await sendChat(text)
    })
    // welcome message
    appendChatMessage('bot', 'Hola — soy el asistente demo. Pregúntame sobre el sistema o el clima.')
  }

  initMap()
  refreshAlerts()
  setInterval(() => { refreshAlerts(); loadZones(); }, 3000)
})

function appendChatMessage(who, text) {
  const list = document.getElementById('chatMessages')
  if (!list) return
  const li = document.createElement('li')
  const div = document.createElement('div')
  div.className = 'msg ' + (who === 'user' ? 'user' : 'bot')
  div.textContent = text
  li.appendChild(div)
  list.appendChild(li)
  // scroll to bottom
  list.scrollTop = list.scrollHeight
}

async function sendChat(text) {
  appendChatMessage('user', text)
  // Add a placeholder bot message (loading)
  const list = document.getElementById('chatMessages')
  const placeholder = document.createElement('li')
  const phdiv = document.createElement('div')
  phdiv.className = 'msg bot'
  phdiv.textContent = 'Pensando...'
  placeholder.appendChild(phdiv)
  list.appendChild(placeholder)
  list.scrollTop = list.scrollHeight

  try {
    const resp = await fetchJSON('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: text }),
    })
    const reply = (resp && resp.reply) ? resp.reply : '(sin respuesta)'
    // replace placeholder text
    phdiv.textContent = reply
  } catch (e) {
    console.error('chat error', e)
    phdiv.textContent = 'Error al contactar el servicio de chat: ' + (e.message || e)
  }
}

