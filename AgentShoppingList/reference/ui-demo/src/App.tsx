import { useState } from 'react'
import { Check, ChevronDown, ChevronRight, ExternalLink, Plus, RotateCcw, Search, ShoppingBag, X } from 'lucide-react'
import { Button } from './components/ui/button'
import { ProductIcon } from './components/product-icon'
import { Checkbox } from './components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from './components/ui/dialog'

import { catalog, categorySpending, formatMoney, formatTripDate, initialList, previousAverageCents, previousAverageLiters, sampleTrips, spendingChangePercent, thisWeek, tripTotal, weeklyPurchases } from './data'
import type { Product, ShoppingTrip } from './data'

export default function App() {
  const [tab, setTab] = useState<'list' | 'products' | 'history' | 'insights'>('list')
  const [items, setItems] = useState(initialList)
  const [search, setSearch] = useState('')
  const [selectedProduct, setSelectedProduct] = useState<Product | null>(null)
  const [expandedTrip, setExpandedTrip] = useState<string | null>('oct-17')
  const [historyNotice, setHistoryNotice] = useState('')
  const checkedCount = items.filter(item => item.checked).length
  const remainingCount = items.length - checkedCount
  const filteredProducts = catalog.filter(product => product.name.toLowerCase().includes(search.toLowerCase()))

  function addProduct(productId: string) {
    setItems(current => current.some(item => item.productId === productId)
      ? current
      : [...current, { productId, quantity: 1, checked: false }])
  }

  function toggleItem(productId: string, checked: boolean) {
    setItems(current => current.map(item => item.productId === productId ? { ...item, checked } : item))
  }

  function buyAgain(trip: ShoppingTrip) {
    setItems(current => {
      const updated = current.map(item => ({ ...item }))
      for (const purchase of trip.items) {
        const existing = updated.find(item => item.productId === purchase.productId)
        if (existing) {
          existing.quantity += purchase.quantity
          existing.checked = false
        } else {
          updated.push({ productId: purchase.productId, quantity: purchase.quantity, checked: false })
        }
      }
      return updated
    })
    setHistoryNotice(`${trip.items.length} products added to your list`)
  }

  const selectedIsAdded = items.some(item => item.productId === selectedProduct?.id)

  return (
    <div className="demo-page">
      <div className="shopping-app">
        <header className="app-header">
          <div className="app-title"><span className="app-icon"><ShoppingBag size={21} strokeWidth={1.8} /></span><h1>Shopping</h1></div>
          <span className="demo-badge">Demo</span>
        </header>

        <nav className="segmented-tabs" aria-label="Shopping views">
          <button className={tab === 'list' ? 'selected' : ''} aria-current={tab === 'list' ? 'page' : undefined} onClick={() => setTab('list')}>My list <span>{items.length}</span></button>
          <button className={tab === 'products' ? 'selected' : ''} aria-current={tab === 'products' ? 'page' : undefined} onClick={() => setTab('products')}>Products</button>
          <button className={tab === 'history' ? 'selected' : ''} aria-current={tab === 'history' ? 'page' : undefined} onClick={() => setTab('history')}>History</button>
          <button className={tab === 'insights' ? 'selected' : ''} aria-current={tab === 'insights' ? 'page' : undefined} onClick={() => setTab('insights')}>Insights</button>
        </nav>

        {tab === 'list' ? (
          <main className="list-screen">
            <div className="section-heading"><h2>Today’s shop</h2><span>{checkedCount} of {items.length} picked up</span></div>
            <div className="shopping-items">
              {items.map(item => {
                const product = catalog.find(product => product.id === item.productId)!
                return (
                  <div className={`shopping-row ${item.checked ? 'picked-up' : ''}`} key={item.productId}>
                    <Checkbox checked={item.checked} onCheckedChange={checked => toggleItem(item.productId, checked === true)} aria-label={`Mark ${product.name} as ${item.checked ? 'not picked up' : 'picked up'}`} />
                    <button className="product-link" onClick={() => setSelectedProduct(product)} aria-label={`View ${product.name} details`}>
                      <span className={`product-image ${product.color}`} aria-hidden="true"><ProductIcon category={product.category} /></span>
                      <span className="product-copy"><strong>{product.name}</strong><span>{product.unit}</span></span>
                    </button>
                    <span className="quantity">×{item.quantity}</span>
                  </div>
                )
              })}
              {items.length === 0 && <div className="empty-state"><ShoppingBag size={26} /><h3>Your list is clear</h3><p>Add something from your saved products.</p></div>}
            </div>
            <Button className="add-products" variant="outline" onClick={() => setTab('products')}><Plus size={16} />Add products</Button>
            <div className="list-status"><span><span className="status-dot" />{remainingCount === 0 && items.length > 0 ? 'All picked up' : `${remainingCount} left`}</span>{checkedCount > 0 && <button onClick={() => setItems(current => current.filter(item => !item.checked))}>Clear checked</button>}</div>
          </main>
        ) : tab === 'products' ? (
          <main className="catalog-screen">
            <div className="catalog-search"><Search size={17} /><input aria-label="Search saved products" placeholder="Search products" value={search} onChange={event => setSearch(event.target.value)} />{search && <button onClick={() => setSearch('')} aria-label="Clear search"><X size={14} /></button>}</div>
            <div className="section-heading"><h2>Saved products</h2><span>{filteredProducts.length} products</span></div>
            <div className="catalog-items">
              {filteredProducts.map(product => {
                const isAdded = items.some(item => item.productId === product.id)
                return (
                  <div className="catalog-row" key={product.id}>
                    <button className="product-link" onClick={() => setSelectedProduct(product)} aria-label={`View ${product.name} details`}>
                      <span className={`product-image ${product.color}`} aria-hidden="true"><ProductIcon category={product.category} /></span>
                      <span className="product-copy"><strong>{product.name}</strong><span>{product.unit}</span></span>
                    </button>
                    <Button className={`catalog-add ${isAdded ? 'is-added' : ''}`} variant="ghost" size="sm" disabled={isAdded} onClick={() => addProduct(product.id)} aria-label={isAdded ? `${product.name} already on your list` : `Add ${product.name} to your list`}>{isAdded ? <><Check size={13} />Added</> : <Plus size={19} />}</Button>
                  </div>
                )
              })}
              {filteredProducts.length === 0 && <div className="empty-state"><Search size={25} /><h3>No products found</h3><p>Try another name.</p></div>}
            </div>
            <Button className="back-to-list" onClick={() => setTab('list')}>View my list <span>{items.length}</span><ChevronRight size={15} /></Button>
          </main>
        ) : tab === 'history' ? (
          <main className="history-screen">
            <div className="section-heading"><h2>Past shops</h2><span>Sample history</span></div>
            <div className="shopping-trips">
              {sampleTrips.map(trip => {
                const isExpanded = expandedTrip === trip.id
                return <section className="trip-card" key={trip.id}>
                  <button className="trip-summary" aria-expanded={isExpanded} aria-controls={`trip-${trip.id}`} onClick={() => setExpandedTrip(isExpanded ? null : trip.id)}>
                    <span className="trip-icon"><ShoppingBag size={18} strokeWidth={1.6} /></span>
                    <span className="trip-store"><strong>{trip.store}</strong><span>{formatTripDate(trip.date)}</span></span>
                    <span className="trip-total"><strong>{formatMoney(tripTotal(trip))}</strong><span>{trip.items.length} products</span></span>
                    <ChevronDown size={15} className={isExpanded ? 'trip-chevron expanded' : 'trip-chevron'} />
                  </button>
                  {isExpanded && <div className="trip-details" id={`trip-${trip.id}`}>
                    {trip.items.map(purchase => {
                      const product = catalog.find(product => product.id === purchase.productId)!
                      return <div className="trip-product" key={purchase.productId}>
                        <span className={`product-image ${product.color}`} aria-hidden="true"><ProductIcon category={product.category} /></span>
                        <div className="product-copy"><strong>{product.name}</strong><span>{product.unit}</span></div>
                        <span className="quantity">×{purchase.quantity}</span>
                      </div>
                    })}
                    <Button className="buy-again" variant="outline" onClick={() => buyAgain(trip)}><RotateCcw size={13} />Buy again</Button>
                  </div>}
                </section>
              })}
            </div>
            {historyNotice && <div className="history-notice" role="status"><Check size={13} />{historyNotice}<button onClick={() => setTab('list')}>View list <ChevronRight size={12} /></button></div>}
          </main>
        ) : <Insights />}
        <footer className="demo-footer">Sample data · Resets on refresh</footer>
      </div>

      <Dialog open={selectedProduct !== null} onOpenChange={open => { if (!open) setSelectedProduct(null) }}>
        {selectedProduct && <DialogContent className="product-dialog">
          <ProductImage key={selectedProduct.id} product={selectedProduct} />
          <DialogTitle className="detail-title">{selectedProduct.name}</DialogTitle>
          <DialogDescription className="detail-description">{selectedProduct.brand ? `${selectedProduct.brand} · ${selectedProduct.flavor}` : 'Sample product'} <span>·</span> {selectedProduct.unit}</DialogDescription>
          <div className="product-description"><h3>Description</h3><p>{selectedProduct.description ?? selectedProduct.note}</p></div>
          <div className="nutrition-heading"><h3>Nutrition</h3><span>{selectedProduct.nutritionBasis ?? `Sample · per 100 ${selectedProduct.id === 'milk' ? 'ml' : 'g'}`}</span></div>
          <div className="nutrition-grid">
            {Object.entries(selectedProduct.nutrition).map(([key, value]) => <div key={key}><span>{key === 'energy' ? selectedProduct.sourceUrl ? 'Calories' : 'Energy' : key[0].toUpperCase() + key.slice(1)}</span><strong>{value}</strong></div>)}
          </div>
          {selectedProduct.sourceUrl && <div className="product-source"><span>Walmart product details</span><a href={selectedProduct.sourceUrl} target="_blank" rel="noreferrer">View at Walmart <ExternalLink size={11} /></a></div>}
          <Button className="detail-add" disabled={selectedIsAdded} onClick={() => addProduct(selectedProduct.id)}>{selectedIsAdded ? <><Check size={16} />On your list</> : <><Plus size={16} />Add to my list</>}</Button>
        </DialogContent>}
      </Dialog>
    </div>
  )
}

function ProductImage({ product }: { product: Product }) {
  const [imageFailed, setImageFailed] = useState(false)
  if (product.imageUrl && !imageFailed) {
    return <div className="detail-image actual-photo"><img src={product.imageUrl} alt={`${product.name}, ${product.unit}`} onError={() => setImageFailed(true)} referrerPolicy="no-referrer" /></div>
  }
  return <div className={`detail-image ${product.color}`}><ProductIcon category={product.category} size={60} />{imageFailed && <span className="image-fallback-note" role="status">Product image unavailable</span>}</div>
}

function Insights() {
  const maximumSpending = Math.max(...weeklyPurchases.map(week => week.spentCents))
  const maximumLiters = Math.max(...weeklyPurchases.map(week => week.drinkLiters))
  const changeDirection = spendingChangePercent <= 0 ? 'less' : 'more'

  return <main className="insights-screen">
    <div className="section-heading"><h2>Purchases at a glance</h2><span>4 weeks</span></div>
    <p className="insights-context">Based on sample purchase history</p>

    <section className="insight-card">
      <div className="insight-heading"><h3>Weekly spending</h3><span>{thisWeek.range}</span></div>
      <div className="spending-metric"><strong>{formatMoney(thisWeek.spentCents)}</strong><span>{Math.abs(spendingChangePercent).toFixed(1)}% {changeDirection}</span></div>
      <p className="metric-comparison">vs {formatMoney(previousAverageCents)} avg. over the prior 3 weeks</p>
      <ul className="spending-chart" aria-label="Weekly purchase spending">
        {weeklyPurchases.map((week, index) => <li key={week.start} aria-label={`${week.range}: ${formatMoney(week.spentCents)}`}>
          <span className="chart-value">{formatMoney(week.spentCents)}</span>
          <div className="spending-bar-track" aria-hidden="true"><span className={index === 3 ? 'spending-bar current' : 'spending-bar'} style={{ height: `${week.spentCents / maximumSpending * 100}%` }} /></div>
          <span className="chart-label">{week.label}</span>
        </li>)}
      </ul>
    </section>

    <section className="insight-card">
      <div className="insight-heading"><h3>Where it went</h3><span>This week</span></div>
      <ul className="category-spending" aria-label="This week’s purchase spending by category">
        {categorySpending.map(category => <li key={category.name}>
          <span className="category-name"><span style={{ background: category.color }} />{category.name}</span>
          <span className="category-bar" aria-hidden="true"><span style={{ width: `${category.spentCents / thisWeek.spentCents * 100}%`, background: category.color }} /></span>
          <strong>{formatMoney(category.spentCents)}</strong>
        </li>)}
      </ul>
      <div className="category-total"><span>Total</span><strong>{formatMoney(thisWeek.spentCents)}</strong></div>
    </section>

    <section className="insight-card">
      <div className="insight-heading"><h3>Drinks bought</h3><span>Liters purchased</span></div>
      <p className="drink-comparison"><strong>{previousAverageLiters.toFixed(0)} L/week</strong> before <span>→</span><strong>{thisWeek.drinkLiters} L</strong> this week</p>
      <p className="metric-comparison">Before = average of the prior 3 weeks · Oat milk</p>
      <ul className="drink-chart" aria-label="Weekly drink purchase volumes">
        {weeklyPurchases.map((week, index) => <li key={week.start} aria-label={`${week.range}: ${week.drinkLiters} liters purchased`}>
          <span>{week.label}</span><span className="drink-bar-track" aria-hidden="true"><span className={index === 3 ? 'current' : ''} style={{ width: `${week.drinkLiters / maximumLiters * 100}%` }} /></span><strong>{week.drinkLiters} L</strong>
        </li>)}
      </ul>
    </section>
  </main>
}
