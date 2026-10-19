import { Apple, Beef, Egg, GlassWater, Wheat } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import type { Category } from '../data'

const categoryIcons: Record<Category, LucideIcon> = {
  'Fruit & veg': Apple,
  Meat: Beef,
  Drinks: GlassWater,
  Pantry: Wheat,
  'Dairy & eggs': Egg,
}

export function ProductIcon({ category, size = 23 }: { category: Category; size?: number }) {
  const Icon = categoryIcons[category]
  return <Icon size={size} strokeWidth={1.6} aria-hidden="true" />
}
