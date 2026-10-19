export type Category = 'Fruit & veg' | 'Meat' | 'Drinks' | 'Pantry' | 'Dairy & eggs'
export type Product = {
  id: string
  name: string
  unit: string
  color: string
  category: Category
  drinkLiters?: number
  note: string
  brand?: string
  flavor?: string
  description?: string
  imageUrl?: string
  sourceUrl?: string
  nutritionBasis?: string
  nutrition: { energy: string; protein?: string; carbs?: string; fat?: string; sugar?: string; sodium?: string }
}
export type ListItem = { productId: string; quantity: number; checked: boolean }
export type ShoppingTrip = {
  id: string
  date: string
  store: string
  items: { productId: string; quantity: number; unitPriceCents: number }[]
}

export const catalog: Product[] = [
  { id: 'avocado', name: 'Avocados', unit: '2 pieces', color: 'green', category: 'Fruit & veg', note: 'Choose ripe ones for this week.', nutrition: { energy: '160 kcal', protein: '2 g', carbs: '8.5 g', fat: '14.7 g' } },
  { id: 'milk', name: 'Oat milk', unit: '1 L carton', color: 'blue', category: 'Drinks', drinkLiters: 1, note: 'Unsweetened, barista style.', nutrition: { energy: '46 kcal', protein: '1 g', carbs: '6.7 g', fat: '1.5 g' } },
  { id: 'eggs', name: 'Free-range eggs', unit: 'Box of 6', color: 'peach', category: 'Dairy & eggs', note: 'Medium eggs.', nutrition: { energy: '143 kcal', protein: '12.6 g', carbs: '0.7 g', fat: '9.5 g' } },
  { id: 'bread', name: 'Sourdough bread', unit: '400 g loaf', color: 'tan', category: 'Pantry', note: 'Sliced, if available.', nutrition: { energy: '250 kcal', protein: '9 g', carbs: '48 g', fat: '1.5 g' } },
  { id: 'berries', name: 'Strawberries', unit: '250 g punnet', color: 'pink', category: 'Fruit & veg', note: 'Keep refrigerated after shopping.', nutrition: { energy: '32 kcal', protein: '0.7 g', carbs: '7.7 g', fat: '0.3 g' } },
  { id: 'banana', name: 'Bananas', unit: '1 bunch', color: 'yellow', category: 'Fruit & veg', note: 'A small bunch.', nutrition: { energy: '89 kcal', protein: '1.1 g', carbs: '22.8 g', fat: '0.3 g' } },
  { id: 'yogurt', name: 'Greek yogurt', unit: '500 g tub', color: 'blue', category: 'Dairy & eggs', note: 'Plain, full fat.', nutrition: { energy: '97 kcal', protein: '9 g', carbs: '4 g', fat: '5 g' } },
  { id: 'tomato', name: 'Cherry tomatoes', unit: '250 g pack', color: 'pink', category: 'Fruit & veg', note: 'On the vine.', nutrition: { energy: '18 kcal', protein: '0.9 g', carbs: '3.9 g', fat: '0.2 g' } },
  { id: 'chicken', name: 'Chicken breast', unit: '500 g pack', color: 'peach', category: 'Meat', note: 'Boneless, skinless.', nutrition: { energy: '120 kcal', protein: '22.5 g', carbs: '0 g', fat: '2.6 g' } },
  {
    id: 'polar-black-cherry', name: 'Polar Black Cherry Seltzer', unit: '8 × 12 fl oz cans', color: 'pink', category: 'Drinks',
    brand: 'Polar', flavor: 'Black Cherry', note: 'Unsweetened sparkling seltzer water.',
    description: 'Black cherry flavored sparkling seltzer water. Unsweetened, with no sugar or sweeteners and zero calories.',
    imageUrl: 'https://i5.walmartimages.com/seo/Polar-Zero-Calorie-Black-Cherry-Sparkling-Seltzer-Water-12-fl-oz-8-Pack-Cans_7763b7ab-8180-4dd8-9397-161757670549.2423cc4bb66c1c83976c4849892e6963.jpeg?odnBg=FFFFFF&odnHeight=573&odnWidth=573',
    sourceUrl: 'https://www.walmart.com/ip/187251201', nutritionBasis: 'Per 12 fl oz can',
    nutrition: { energy: '0', sugar: '0 g', carbs: '0 g', sodium: '0 mg' },
  },
]
export const initialList: ListItem[] = ['avocado', 'polar-black-cherry', 'milk', 'eggs', 'bread', 'berries'].map(productId => ({
  productId,
  quantity: productId === 'milk' ? 2 : 1,
  checked: productId === 'berries',
}))

// All history and insights use these same sample line items, including their prices.
export const sampleTrips: ShoppingTrip[] = [
  { id: 'oct-17', date: '2026-10-17', store: 'Walmart', items: [
    { productId: 'milk', quantity: 2, unitPriceCents: 349 },
    { productId: 'bread', quantity: 1, unitPriceCents: 499 },
    { productId: 'banana', quantity: 1, unitPriceCents: 179 },
    { productId: 'yogurt', quantity: 1, unitPriceCents: 449 },
    { productId: 'chicken', quantity: 1, unitPriceCents: 1021 },
  ] },
  { id: 'oct-12', date: '2026-10-12', store: 'Trader Joe’s', items: [
    { productId: 'eggs', quantity: 1, unitPriceCents: 429 },
    { productId: 'berries', quantity: 2, unitPriceCents: 449 },
    { productId: 'tomato', quantity: 1, unitPriceCents: 345 },
    { productId: 'avocado', quantity: 1, unitPriceCents: 300 },
  ] },
  { id: 'oct-10', date: '2026-10-10', store: 'Walmart', items: [
    { productId: 'milk', quantity: 4, unitPriceCents: 349 },
    { productId: 'chicken', quantity: 2, unitPriceCents: 1021 },
    { productId: 'yogurt', quantity: 2, unitPriceCents: 449 },
    { productId: 'banana', quantity: 2, unitPriceCents: 179 },
    { productId: 'tomato', quantity: 2, unitPriceCents: 345 },
    { productId: 'bread', quantity: 1, unitPriceCents: 499 },
  ] },
  { id: 'oct-03', date: '2026-10-03', store: 'Walmart', items: [
    { productId: 'milk', quantity: 5, unitPriceCents: 349 },
    { productId: 'chicken', quantity: 2, unitPriceCents: 1021 },
    { productId: 'eggs', quantity: 2, unitPriceCents: 429 },
    { productId: 'avocado', quantity: 2, unitPriceCents: 300 },
    { productId: 'bread', quantity: 1, unitPriceCents: 499 },
  ] },
  { id: 'sep-26', date: '2026-09-26', store: 'Trader Joe’s', items: [
    { productId: 'milk', quantity: 6, unitPriceCents: 349 },
    { productId: 'chicken', quantity: 2, unitPriceCents: 1021 },
    { productId: 'bread', quantity: 2, unitPriceCents: 499 },
    { productId: 'berries', quantity: 2, unitPriceCents: 449 },
    { productId: 'yogurt', quantity: 1, unitPriceCents: 449 },
  ] },
]

export const formatMoney = (cents: number) => `$${(cents / 100).toFixed(2)}`
export const formatTripDate = (date: string) => new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${date}T12:00:00Z`))
export const tripTotal = (trip: ShoppingTrip) => trip.items.reduce((total, item) => total + item.quantity * item.unitPriceCents, 0)

// The demo uses four fixed Monday–Sunday weeks ending Oct 18, 2026.
const demoWeeks = [
  { label: 'Sep 21', range: 'Sep 21–27', start: '2026-09-21', end: '2026-09-27' },
  { label: 'Sep 28', range: 'Sep 28–Oct 4', start: '2026-09-28', end: '2026-10-04' },
  { label: 'Oct 5', range: 'Oct 5–11', start: '2026-10-05', end: '2026-10-11' },
  { label: 'Oct 12', range: 'Oct 12–18', start: '2026-10-12', end: '2026-10-18' },
]
export const weeklyPurchases = demoWeeks.map(week => {
  const trips = sampleTrips.filter(trip => trip.date >= week.start && trip.date <= week.end)
  return {
    ...week,
    spentCents: trips.reduce((total, trip) => total + tripTotal(trip), 0),
    drinkLiters: trips.flatMap(trip => trip.items).reduce((total, item) => total + item.quantity * (catalog.find(product => product.id === item.productId)?.drinkLiters ?? 0), 0),
  }
})
export const thisWeek = weeklyPurchases[3]
export const previousAverageCents = weeklyPurchases.slice(0, 3).reduce((total, week) => total + week.spentCents, 0) / 3
export const previousAverageLiters = weeklyPurchases.slice(0, 3).reduce((total, week) => total + week.drinkLiters, 0) / 3
export const spendingChangePercent = (thisWeek.spentCents - previousAverageCents) / previousAverageCents * 100
const categories: { name: Category; color: string }[] = [
  { name: 'Fruit & veg', color: '#8fa87b' },
  { name: 'Meat', color: '#d2a38d' },
  { name: 'Drinks', color: '#9bb4bc' },
  { name: 'Pantry', color: '#c4b48b' },
  { name: 'Dairy & eggs', color: '#acb3c4' },
]
export const categorySpending = categories.map(category => ({
  ...category,
  spentCents: sampleTrips.filter(trip => trip.date >= thisWeek.start && trip.date <= thisWeek.end)
    .flatMap(trip => trip.items)
    .filter(item => catalog.find(product => product.id === item.productId)?.category === category.name)
    .reduce((total, item) => total + item.quantity * item.unitPriceCents, 0),
}))
